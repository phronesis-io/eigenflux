#!/bin/bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
DEPLOY_USER="${DEPLOY_USER:-${SUDO_USER:-}}"
LIB_DIR="/usr/local/libexec/eigenflux"
BIN_PATH="/usr/local/sbin/eigenflux-deploy-main"
UNIT_PATH="/etc/systemd/system/eigenflux-deploy-main.service"
SUDOERS_PATH="/etc/sudoers.d/eigenflux-deploy-main"
POLICY_DIR="/etc/eigenflux"
STATE_DIR="/var/lib/eigenflux-deployer"
APP_DROPIN_DIR="/etc/systemd/system/eigenflux-app@.service.d"
RUNTIME_ENV="${POLICY_DIR}/runtime.env"
FRIEND_REQUEST_LIMITS_CONFIG="${POLICY_DIR}/friend_request_limits.yaml"
LEGACY_FRIEND_REQUEST_LIMITS_CONFIG="${PROJECT_ROOT}/configs/pm/friend_request_limits.yaml"
GITHUB_KNOWN_HOSTS="${POLICY_DIR}/github_known_hosts"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Please run with sudo: sudo DEPLOY_USER=<operator> $0" >&2
  exit 1
fi

if [[ "${PROJECT_ROOT}" != /* ]] || ! git -C "${PROJECT_ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "Installer must run from an EigenFlux Git worktree." >&2
  exit 1
fi
if [[ -n "$(git -C "${PROJECT_ROOT}" status --porcelain --untracked-files=all)" ]]; then
  echo "Refusing installation from a dirty worktree." >&2
  exit 1
fi
[[ "${DEPLOY_USER}" =~ ^[a-z_][a-z0-9_-]*$ ]] || {
  echo "DEPLOY_USER must name the non-root deployment operator." >&2
  exit 1
}
id "${DEPLOY_USER}" >/dev/null
DEPLOY_GROUP="$(id -gn "${DEPLOY_USER}")"
DEPLOY_HOME="$(getent passwd "${DEPLOY_USER}" | cut -d: -f6)"
[[ "${DEPLOY_HOME}" == /* && -d "${DEPLOY_HOME}" ]] || {
  echo "Could not determine a valid home directory for ${DEPLOY_USER}." >&2
  exit 1
}
command -v visudo >/dev/null
command -v ssh-keygen >/dev/null
[[ -f "${PROJECT_ROOT}/.env" ]] || {
  echo "Production .env is required before installing the deployment gate." >&2
  exit 1
}
[[ -f "${DEPLOY_HOME}/.ssh/known_hosts" ]] || {
  echo "${DEPLOY_USER} must connect to github.com once before installation." >&2
  exit 1
}
if [[ ! -f "${FRIEND_REQUEST_LIMITS_CONFIG}" && ! -f "${LEGACY_FRIEND_REQUEST_LIMITS_CONFIG}" ]]; then
  echo "Friend-request rate-limit config is required: ${FRIEND_REQUEST_LIMITS_CONFIG}" >&2
  echo "Create it from configs/pm/friend_request_limits.example.yaml before installing." >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

mkdir -p "${LIB_DIR}" "${POLICY_DIR}" "${STATE_DIR}/releases" "${STATE_DIR}/work" "${APP_DROPIN_DIR}"
chmod 0755 "${STATE_DIR}" "${STATE_DIR}/releases" "${STATE_DIR}/work"
for binary in profile item sort feed pm auth notification api ws pipeline cron; do
  [[ -f "${PROJECT_ROOT}/build/${binary}" && -x "${PROJECT_ROOT}/build/${binary}" ]] || {
    echo "Missing build/${binary}; build all production services before installation." >&2
    exit 1
  }
done
ssh-keygen -F github.com -f "${DEPLOY_HOME}/.ssh/known_hosts" | \
  awk '!/^#/' > "${tmp_dir}/github_known_hosts"
[[ -s "${tmp_dir}/github_known_hosts" ]] || {
  echo "No trusted github.com host key found for ${DEPLOY_USER}." >&2
  exit 1
}
install -o root -g root -m 0644 "${tmp_dir}/github_known_hosts" "${GITHUB_KNOWN_HOSTS}"
install -o root -g root -m 0600 "${PROJECT_ROOT}/.env" "${RUNTIME_ENV}"
if [[ -f "${FRIEND_REQUEST_LIMITS_CONFIG}" ]]; then
  chown root:"${DEPLOY_GROUP}" "${FRIEND_REQUEST_LIMITS_CONFIG}"
  chmod 0640 "${FRIEND_REQUEST_LIMITS_CONFIG}"
elif [[ -f "${LEGACY_FRIEND_REQUEST_LIMITS_CONFIG}" ]]; then
  install -o root -g "${DEPLOY_GROUP}" -m 0640 \
    "${LEGACY_FRIEND_REQUEST_LIMITS_CONFIG}" "${FRIEND_REQUEST_LIMITS_CONFIG}"
else
  echo "Friend-request rate-limit config is required: ${FRIEND_REQUEST_LIMITS_CONFIG}" >&2
  echo "Create it from configs/pm/friend_request_limits.example.yaml before installing." >&2
  exit 1
fi
# Keep the application's legacy .env lookup read-only and identical to the
# root-managed deployment environment.
install -o root -g "${DEPLOY_GROUP}" -m 0640 "${RUNTIME_ENV}" "${PROJECT_ROOT}/.env"

bootstrap_source="${STATE_DIR}/work/bootstrap-$(date +%s)"
mkdir -p "${bootstrap_source}"
chmod 0755 "${bootstrap_source}"
GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null \
  GIT_NO_REPLACE_OBJECTS=1 git -C "${PROJECT_ROOT}" archive HEAD | \
  tar -x -C "${bootstrap_source}"
install -o root -g root -m 0600 "${RUNTIME_ENV}" "${bootstrap_source}/.env"
install -o root -g root -m 0644 \
  "${PROJECT_ROOT}/scripts/cloud/deploy_main_lib.sh" \
  "${LIB_DIR}/deploy_main_lib.sh"

sed \
  -e "s#@@PROJECT_ROOT@@#${PROJECT_ROOT}#g" \
  -e "s#@@DEPLOY_LIB@@#${LIB_DIR}/deploy_main_lib.sh#g" \
  -e "s#@@DEPLOY_USER@@#${DEPLOY_USER}#g" \
  -e "s#@@DEPLOY_HOME@@#${DEPLOY_HOME}#g" \
  "${PROJECT_ROOT}/scripts/cloud/deploy_main.sh" > "${tmp_dir}/eigenflux-deploy-main"
install -o root -g root -m 0755 "${tmp_dir}/eigenflux-deploy-main" "${BIN_PATH}"

install -o root -g root -m 0644 \
  "${PROJECT_ROOT}/cloud/systemd/eigenflux-deploy-main.service.tpl" \
  "${UNIT_PATH}"
install -o root -g root -m 0644 \
  "${PROJECT_ROOT}/cloud/systemd/eigenflux-app-deployer.conf.tpl" \
  "${APP_DROPIN_DIR}/deployer.conf"
install -o root -g root -m 0644 \
  "${PROJECT_ROOT}/scripts/cloud/DEPLOYMENT_POLICY.md" \
  "${POLICY_DIR}/DEPLOYMENT_POLICY.md"

cat > "${tmp_dir}/sudoers" <<EOF
${DEPLOY_USER} ALL=(root) NOPASSWD: /usr/bin/systemctl start eigenflux-deploy-main.service
EOF
visudo -cf "${tmp_dir}/sudoers"
install -o root -g root -m 0440 "${tmp_dir}/sudoers" "${SUDOERS_PATH}"

# The drop-in points services at a root-owned artifact directory. Bootstrap it
# from the currently built binaries so installing the gate does not break the
# next ordinary service restart.
bootstrap="${STATE_DIR}/releases/bootstrap-$(date +%s)"
mkdir -p "${bootstrap}/bin"
chmod 0755 "${bootstrap}" "${bootstrap}/bin"
for binary in profile item sort feed pm auth notification api ws pipeline cron; do
  [[ -f "${PROJECT_ROOT}/build/${binary}" && -x "${PROJECT_ROOT}/build/${binary}" ]] || {
    echo "Missing build/${binary}; build all production services before installation." >&2
    exit 1
  }
  install -o root -g root -m 0755 "${PROJECT_ROOT}/build/${binary}" "${bootstrap}/bin/${binary}"
done
ln -s "${bootstrap_source}" "${bootstrap}/source"
bootstrap_link="${STATE_DIR}/.current.$$"
ln -s "${bootstrap}" "${bootstrap_link}"
if mv --help >/dev/null 2>&1; then
  mv -Tf "${bootstrap_link}" "${STATE_DIR}/current"
else
  mv -fh "${bootstrap_link}" "${STATE_DIR}/current"
fi

systemctl daemon-reload
echo "Installed root-managed EigenFlux deployment entrypoint."
echo "Deploy latest main: sudo systemctl start eigenflux-deploy-main.service"
echo "Logs: journalctl -u eigenflux-deploy-main.service"
echo "Rollback is root-only: ${BIN_PATH} --rollback <full-main-commit-sha>"
