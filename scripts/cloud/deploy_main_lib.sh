#!/bin/bash

# Shared implementation for the root-owned production deploy wrapper.
# This file is installed under /usr/local/libexec/eigenflux and is not sourced
# from the production checkout.

deploy_main_as_user() {
  local deploy_user=$1
  local deploy_home=$2
  shift 2
  if [[ -n "${deploy_user}" ]]; then
    runuser -u "${deploy_user}" -- env -i \
      HOME="${deploy_home}" USER="${deploy_user}" LOGNAME="${deploy_user}" \
      PATH="${PATH}" TMPDIR="${TMPDIR:-/tmp}" "$@"
  else
    env -i HOME="${HOME:-/}" USER="${USER:-}" LOGNAME="${LOGNAME:-}" \
      PATH="${PATH}" TMPDIR="${TMPDIR:-/tmp}" "$@"
  fi
}

deploy_main_git_as_user() {
  local deploy_user=$1
  local deploy_home=$2
  shift 2
  deploy_main_as_user "${deploy_user}" "${deploy_home}" env \
    GIT_CONFIG_NOSYSTEM=1 \
    GIT_CONFIG_SYSTEM=/dev/null \
    GIT_CONFIG_GLOBAL=/dev/null \
    GIT_NO_REPLACE_OBJECTS=1 \
    GIT_SSH_COMMAND='/usr/bin/ssh -F /dev/null -o HostName=github.com -o User=git -o IdentitiesOnly=yes -o UserKnownHostsFile=/etc/eigenflux/github_known_hosts -o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=yes' \
    git "$@"
}

deploy_main_assert_clean() {
  local project_root=$1
  local phase=$2
  local deploy_user=$3
  local deploy_home=$4
  local dirty

  dirty="$(deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" status --porcelain --untracked-files=all)" || return 1
  if [[ -n "${dirty}" ]]; then
    echo "Refusing deployment: production worktree is dirty (${phase})." >&2
    printf '%s\n' "${dirty}" >&2
    return 1
  fi
}

deploy_main_restart_services() {
  local units=(
    eigenflux-etcd
    eigenflux-app@profile
    eigenflux-app@item
    eigenflux-app@sort
    eigenflux-app@feed
    eigenflux-app@pm
    eigenflux-app@auth
    eigenflux-app@notification
    eigenflux-app@api
    eigenflux-app@ws
    eigenflux-app@pipeline
    eigenflux-app@cron
  )
  local unit
  for unit in "${units[@]}"; do
    echo "Restarting ${unit}"
    systemctl restart "${unit}" || return 1
    systemctl is-active --quiet "${unit}" || return 1
  done
}

deploy_main_stage_build() {
  local project_root=$1
  local state_dir=$2
  local target=$3
  local source_dir=$4
  local binaries=(profile item sort feed pm auth notification api ws pipeline cron)
  local release_dir
  local binary

  mkdir -p "${state_dir}/releases"
  chmod 0755 "${state_dir}" "${state_dir}/releases" || return 1
  release_dir="$(mktemp -d "${state_dir}/releases/${target}.XXXXXX")" || return 1
  mkdir -p "${release_dir}/bin"
  chmod 0755 "${release_dir}" "${release_dir}/bin" || return 1
  for binary in "${binaries[@]}"; do
    [[ -f "${project_root}/build/${binary}" && -x "${project_root}/build/${binary}" ]] || {
      echo "Missing build artifact: build/${binary}" >&2
      return 1
    }
    install -m 0755 "${project_root}/build/${binary}" "${release_dir}/bin/${binary}" || return 1
  done
  ln -s "${source_dir}" "${release_dir}/source" || return 1
  printf '%s\n' "${release_dir}"
}

deploy_main_activate_build() {
  local state_dir=$1
  local release_dir=$2
  local next_link="${state_dir}/.current.$$"

  ln -s "${release_dir}" "${next_link}" || return 1
  if mv --help >/dev/null 2>&1; then
    mv -Tf "${next_link}" "${state_dir}/current" || return 1
  else
    mv -fh "${next_link}" "${state_dir}/current" || return 1
  fi
}

deploy_main_prepare_source() {
  local project_root=$1
  local state_dir=$2
  local target=$3
  local runtime_env=$4
  local deploy_user=$5
  local deploy_home=$6
  local source_dir

  mkdir -p "${state_dir}/work"
  chmod 0755 "${state_dir}" "${state_dir}/work" || return 1
  source_dir="$(mktemp -d "${state_dir}/work/${target}.XXXXXX")" || return 1
  chmod 0755 "${source_dir}" || return 1
  deploy_main_git_as_user "${deploy_user}" "${deploy_home}" \
    -C "${project_root}" archive "${target}" | \
    tar -x -C "${source_dir}" || return 1
  [[ -f "${runtime_env}" ]] || {
    echo "Root-managed runtime environment is missing: ${runtime_env}" >&2
    return 1
  }
  install -m 0600 "${runtime_env}" "${source_dir}/.env" || return 1
  printf '%s\n' "${source_dir}"
}

deploy_main_run() {
  local project_root=$1
  local lock_file=$2
  local deploy_user=$3
  local deploy_home=$4
  local official_remote=$5
  local state_dir=$6
  local runtime_env=$7
  shift 7

  local mode="latest"
  local requested_sha=""
  case "${1:-}" in
    "") ;;
    --rollback)
      mode="rollback"
      requested_sha="${2:-}"
      [[ $# -eq 2 ]] || {
        echo "Usage: eigenflux-deploy-main [--rollback <main-commit-sha>]" >&2
        return 2
      }
      [[ "${requested_sha}" =~ ^[0-9a-fA-F]{40}$ ]] || {
        echo "Rollback requires a full 40-character commit SHA." >&2
        return 2
      }
      ;;
    *)
      echo "Usage: eigenflux-deploy-main [--rollback <main-commit-sha>]" >&2
      return 2
      ;;
  esac

  deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
    echo "Production repository not found: ${project_root}" >&2
    return 1
  }

  exec 9>"${lock_file}"
  flock -n 9 || {
    echo "Another EigenFlux deployment is already running." >&2
    return 1
  }

  deploy_main_assert_clean "${project_root}" "before fetch" "${deploy_user}" "${deploy_home}" || return 1
  local remote_line origin_main target
  remote_line="$(deploy_main_git_as_user "${deploy_user}" "${deploy_home}" ls-remote \
    "${official_remote}" refs/heads/main)" || return 1
  origin_main="${remote_line%%[[:space:]]*}"
  [[ "${origin_main}" =~ ^[0-9a-f]{40}$ ]] || {
    echo "Official main did not resolve to one full commit SHA." >&2
    return 1
  }
  deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" fetch --no-tags \
    "${official_remote}" "${origin_main}" || return 1
  deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" cat-file -e "${origin_main}^{commit}" || return 1

  target="${origin_main}"

  if [[ "${mode}" == "rollback" ]]; then
    deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" fetch --no-tags \
      "${official_remote}" "${requested_sha}" || return 1
    target="$(deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" rev-parse --verify "${requested_sha}^{commit}")" || return 1
    deploy_main_git_as_user "${deploy_user}" "${deploy_home}" -C "${project_root}" merge-base --is-ancestor "${target}" "${origin_main}" || {
      echo "Refusing rollback: ${target} is not contained in origin/main." >&2
      return 1
    }
  fi

  echo "Deploying ${target} from origin/main (${mode})."
  local source_dir release_dir
  source_dir="$(deploy_main_prepare_source "${project_root}" "${state_dir}" "${target}" \
    "${runtime_env}" "${deploy_user}" "${deploy_home}")" || return 1
  bash "${source_dir}/scripts/common/build.sh" || return 1
  release_dir="$(deploy_main_stage_build "${source_dir}" "${state_dir}" "${target}" "${source_dir}")" || return 1

  if [[ "${mode}" == "latest" ]]; then
    bash "${source_dir}/scripts/common/migrate_up.sh" || return 1
  else
    echo "Rollback mode: database migrations are intentionally unchanged."
  fi

  deploy_main_assert_clean "${project_root}" "before restart" "${deploy_user}" "${deploy_home}" || return 1
  deploy_main_activate_build "${state_dir}" "${release_dir}" || return 1
  deploy_main_restart_services || return 1
  bash "${source_dir}/scripts/cloud/check_services.sh" || return 1

  deploy_main_assert_clean "${project_root}" "after deployment" "${deploy_user}" "${deploy_home}" || return 1
  printf '%s\n' "${target}" > "${state_dir}/deployed-sha"

  echo "EigenFlux deployment completed: ${target}"
}
