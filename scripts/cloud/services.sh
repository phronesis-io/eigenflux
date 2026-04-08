#!/bin/bash
# Single source of truth for all cloud service definitions.
# Sourced by restart_all_services.sh, check_services.sh, restart.sh, logs.sh
#
# When adding a new service, update ALL_MODULES and module_port() here only.

# Ordered list of all modules (controls startup order).
ALL_MODULES=(etcd profile item sort feed pm auth notification api ws pipeline cron)

# Map module name → systemd unit name.
module_to_unit() {
  case "$1" in
    etcd) echo "eigenflux-etcd" ;;
    *)    echo "eigenflux-app@$1" ;;
  esac
}

# Map module name → listening port (empty if none).
# Relies on env vars (source .env before calling).
module_port() {
  case "$1" in
    api)          echo "${API_PORT:-8080}" ;;
    ws)           echo "${WS_PORT:-8088}" ;;
    profile)      echo "${PROFILE_RPC_PORT:-8881}" ;;
    item)         echo "${ITEM_RPC_PORT:-8882}" ;;
    sort)         echo "${SORT_RPC_PORT:-8883}" ;;
    feed)         echo "${FEED_RPC_PORT:-8884}" ;;
    pm)           echo "${PM_RPC_PORT:-8885}" ;;
    auth)         echo "${AUTH_RPC_PORT:-8886}" ;;
    notification) echo "${NOTIFICATION_RPC_PORT:-8887}" ;;
    *)            echo "" ;;
  esac
}

# Check if a module name is valid.
is_valid_module() {
  local mod="$1"
  for m in "${ALL_MODULES[@]}"; do
    [[ "$m" == "$mod" ]] && return 0
  done
  return 1
}

# Print module list (for usage messages).
print_modules() {
  for m in "${ALL_MODULES[@]}"; do
    echo "  $m"
  done
}
