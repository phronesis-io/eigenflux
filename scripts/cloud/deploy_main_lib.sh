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

deploy_main_release_complete() {
  local release_dir=$1
  local binary
  [[ -d "${release_dir}/bin" && -L "${release_dir}/source" ]] || return 1
  for binary in profile item sort feed pm auth notification api ws pipeline cron; do
    [[ -x "${release_dir}/bin/${binary}" ]] || return 1
  done
}

deploy_main_prune_artifacts() {
  local state_dir=$1
  local retain_count=${2:-2}
  local current_release current_source candidate source_dir path
  local -a keep_releases keep_sources
  local removed_releases=0
  local removed_work=0

  [[ "${retain_count}" =~ ^[1-9][0-9]*$ ]] || {
    echo "Deployment artifact retention must be a positive integer." >&2
    return 1
  }

  state_dir="$(cd "${state_dir}" && pwd -P)" || return 1

  current_release="$(readlink -f "${state_dir}/current")" || return 1
  case "${current_release}" in
    "${state_dir}/releases/"*) ;;
    *)
      echo "Refusing artifact prune: current release is outside ${state_dir}/releases." >&2
      return 1
      ;;
  esac
  deploy_main_release_complete "${current_release}" || {
    echo "Refusing artifact prune: current release is incomplete." >&2
    return 1
  }

  keep_releases=("${current_release}")
  while IFS= read -r candidate; do
    [[ "${candidate}" == "${current_release}" ]] && continue
    deploy_main_release_complete "${candidate}" || continue
    keep_releases+=("${candidate}")
    [[ ${#keep_releases[@]} -ge ${retain_count} ]] && break
  done < <(LC_ALL=C ls -1dt "${state_dir}"/releases/* 2>/dev/null || true)

  keep_sources=()
  for candidate in "${keep_releases[@]}"; do
    source_dir="$(readlink -f "${candidate}/source")" || return 1
    case "${source_dir}" in
      "${state_dir}/work/"*) ;;
      *)
        echo "Refusing artifact prune: release source is outside ${state_dir}/work." >&2
        return 1
        ;;
    esac
    [[ -d "${source_dir}" ]] || {
      echo "Refusing artifact prune: release source is missing: ${source_dir}" >&2
      return 1
    }
    keep_sources+=("${source_dir}")
  done
  current_source="${keep_sources[0]}"

  for path in "${state_dir}"/releases/*; do
    [[ -d "${path}" ]] || continue
    for candidate in "${keep_releases[@]}"; do
      [[ "${path}" == "${candidate}" ]] && continue 2
    done
    rm -rf "${path}" || return 1
    removed_releases=$((removed_releases + 1))
  done

  for path in "${state_dir}"/work/*; do
    [[ -d "${path}" ]] || continue
    for source_dir in "${keep_sources[@]}"; do
      [[ "${path}" == "${source_dir}" ]] && continue 2
    done
    rm -rf "${path}" || return 1
    removed_work=$((removed_work + 1))
  done

  [[ "$(readlink -f "${state_dir}/current")" == "${current_release}" ]]
  [[ "$(readlink -f "${state_dir}/current/source")" == "${current_source}" ]]
  echo "Deployment artifact retention: kept ${#keep_releases[@]} release(s); removed ${removed_releases} release(s) and ${removed_work} work tree(s)."
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

# API releases have their own source and pointer: changing the shared current
# bundle would also change relative resources for still-running RPC services.
deploy_main_api_matches() {
  local pid
  pid="$(systemctl show eigenflux-app@api -p MainPID --value)" || return 1
  [[ "${pid}" =~ ^[1-9][0-9]*$ ]] || return 1
  cmp -s "/proc/${pid}/exe" "$1/bin/api"
}

deploy_main_api_only() {
  local source_dir=$1 state_dir=$2 target=$3
  local override_dir=${4:-/etc/systemd/system/eigenflux-app@api.service.d}
  # systemd sorts template and instance drop-ins together by filename.
  # This must sort after the shared deployer.conf.
  local override_file=${override_dir}/zz-api-only.conf
  local release_dir old_link="" had_override=0
  mkdir -p "${state_dir}/api-releases" || return 1
  release_dir="$(mktemp -d "${state_dir}/api-releases/${target}.XXXXXX")" || return 1
  chmod 0755 "${release_dir}" || return 1
  mv "${source_dir}" "${release_dir}/source" || return 1
  mkdir -p "${release_dir}/bin" || return 1
  (cd "${release_dir}/source" && go build -o "${release_dir}/bin/api" ./api) || return 1
  printf '%s\n' "${target}" > "${release_dir}/revision" || return 1
  # Require a healthy old API before changing its unit or executable.
  systemctl is-active --quiet eigenflux-app@api || return 1
  curl --max-time 10 -fsS http://127.0.0.1:8080/api/v1/website/stats >/dev/null || return 1
  if [[ -e "${override_file}" ]]; then
    had_override=1
    cp -p "${override_file}" "${release_dir}/previous-override" || return 1
  fi
  if [[ -L "${state_dir}/api-current" ]]; then
    old_link="$(readlink "${state_dir}/api-current")"
  elif [[ -e "${state_dir}/api-current" ]]; then
    echo "Refusing non-symlink api-current" >&2
    return 1
  fi
  mkdir -p "${override_dir}" || return 1
  printf '[Service]\nWorkingDirectory=%s/api-current/source\nExecStartPre=\nExecStart=\nExecStartPre=/usr/bin/test -x %s/api-current/bin/api\nExecStart=%s/api-current/bin/api\n' \
    "${state_dir}" "${state_dir}" "${state_dir}" > "${release_dir}/override" || return 1
  if install -m 0644 "${release_dir}/override" "${override_file}" &&
    ln -s "${release_dir}" "${state_dir}/.api-next.$$" &&
    mv -Tf "${state_dir}/.api-next.$$" "${state_dir}/api-current" &&
    systemctl daemon-reload &&
    bash "${release_dir}/source/scripts/cloud/restart.sh" api &&
    curl --retry 15 --retry-delay 1 --retry-connrefused --max-time 3 -fsS \
      http://127.0.0.1:8080/api/v1/website/stats >/dev/null &&
    deploy_main_api_matches "${release_dir}"; then
    printf '%s\n' "${target}" > "${state_dir}/api-deployed-sha"
    deploy_main_record_annotations "${release_dir}/source" "${state_dir}" "${target}" api
    echo "API-only deployment completed: ${target}; other services and migrations unchanged"
    return 0
  fi
  echo "API health check failed; restoring previous API configuration" >&2
  if [[ "${had_override}" == 1 ]]; then
    install -m 0644 "${release_dir}/previous-override" "${override_file}" || return 1
  else
    rm -f "${override_file}" || return 1
  fi
  if [[ -n "${old_link}" ]]; then
    ln -s "${old_link}" "${state_dir}/.api-rollback.$$" || return 1
    mv -Tf "${state_dir}/.api-rollback.$$" "${state_dir}/api-current" || return 1
  fi
  systemctl daemon-reload
  bash "${release_dir}/source/scripts/cloud/restart.sh" api
  return 1
}

# The installed root-owned helper is independent of the release being rolled back.
deploy_main_annotation_tool() {
  python3 /usr/local/libexec/eigenflux/release_annotations.py "$@"
}

deploy_main_record_annotations() {
  local source_dir=$1 state_dir=$2 target=$3 scope=$4
  if ! deploy_main_annotation_tool --state-dir "${state_dir}/annotations" record \
    --manifest "${source_dir}/deploy/release-events.json" --scope "${scope}" --commit "${target}"; then
    echo "WARNING: healthy deployment annotation receipt failed; operator repair required for ${target}." >&2
    return 0
  fi
  # An inactive/misconfigured worker does not roll back healthy application code.
  systemctl start --no-block eigenflux-release-annotations.service || \
    echo "WARNING: release annotation writer unavailable; durable events await the retry timer." >&2
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
    --api-only)
      [[ $# -eq 1 ]] || return 2
      mode="api-only"
      ;;
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
  deploy_main_annotation_tool --state-dir "${state_dir}/annotations" validate \
    --manifest "${source_dir}/deploy/release-events.json" || return 1
  if [[ "${mode}" == "api-only" ]]; then
    deploy_main_assert_clean "${project_root}" "before API build" "${deploy_user}" "${deploy_home}" || return 1
    deploy_main_api_only "${source_dir}" "${state_dir}" "${target}" || return 1
    deploy_main_assert_clean "${project_root}" "after API deployment" "${deploy_user}" "${deploy_home}"
    return $?
  fi
  if ! bash "${source_dir}/scripts/common/build.sh"; then
    rm -rf "${source_dir}"
    return 1
  fi
  release_dir="$(deploy_main_stage_build "${source_dir}" "${state_dir}" "${target}" "${source_dir}")" || return 1

  if [[ "${mode}" == "latest" ]]; then
    bash "${source_dir}/scripts/common/migrate_up.sh" || return 1
  else
    echo "Rollback mode: database migrations are intentionally unchanged."
  fi

  deploy_main_assert_clean "${project_root}" "before restart" "${deploy_user}" "${deploy_home}" || return 1
  deploy_main_activate_build "${state_dir}" "${release_dir}" || return 1
  # A later full release must update API instances previously released alone.
  if [[ -L "${state_dir}/api-current" ]]; then
    ln -s "${release_dir}" "${state_dir}/.api-full.$$" || return 1
    mv -Tf "${state_dir}/.api-full.$$" "${state_dir}/api-current" || return 1
  fi
  deploy_main_restart_services || return 1
  bash "${source_dir}/scripts/cloud/check_services.sh" || return 1

  deploy_main_assert_clean "${project_root}" "after deployment" "${deploy_user}" "${deploy_home}" || return 1
  printf '%s\n' "${target}" > "${state_dir}/deployed-sha"
  deploy_main_record_annotations "${source_dir}" "${state_dir}" "${target}" full
  deploy_main_prune_artifacts "${state_dir}" 2 || return 1

  echo "EigenFlux deployment completed: ${target}"
}
