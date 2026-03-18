#!/usr/bin/env bash
set -euo pipefail

MODE="server"
LOG_DIR=""
SPINNER_PID=""
SPINNER_MSG=""
SPINNER_FRAMES=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')

CYAN=$'\033[36m'
GREEN=$'\033[32m'
RED=$'\033[31m'
RESET=$'\033[0m'

cleanup() {
  stop_spinner
  if [[ -n "$LOG_DIR" && -d "$LOG_DIR" ]]; then
    rm -rf "$LOG_DIR"
  fi
}

trap cleanup EXIT INT HUP TERM

start_spinner() {
  SPINNER_MSG="$1"
  if [[ ! -t 1 ]]; then
    return
  fi

  (
    local index=0
    while true; do
      printf '\r%s%s%s %s ' "$CYAN" "${SPINNER_FRAMES[index]}" "$RESET" "$SPINNER_MSG"
      sleep 0.1
      index=$(( (index + 1) % ${#SPINNER_FRAMES[@]} ))
    done
  ) &
  SPINNER_PID=$!
}

stop_spinner() {
  if [[ -n "$SPINNER_PID" ]]; then
    kill "$SPINNER_PID" 2>/dev/null || true
    wait "$SPINNER_PID" 2>/dev/null || true
    SPINNER_PID=""
    if [[ -t 1 ]]; then
      printf '\r\033[K'
    fi
  fi
}

step_done() {
  stop_spinner
  printf '%s✓%s %s\n' "$GREEN" "$RESET" "$1"
}

step_fail() {
  stop_spinner
  printf '%s✗%s %s\n' "$RED" "$RESET" "$1" >&2
}

run_step() {
  local message="$1"
  shift

  local log_file="$LOG_DIR/$(date +%s%N).log"
  if [[ ! -t 1 ]]; then
    printf '%s...\n' "$message"
  fi
  start_spinner "$message"

  if "$@" >"$log_file" 2>&1; then
    step_done "$message"
    return 0
  fi

  local status=$?
  step_fail "$message"
  if [[ -s "$log_file" ]]; then
    printf 'Command output:\n' >&2
    cat "$log_file" >&2
  fi
  exit "$status"
}

install_repository_key() {
  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
}

write_repository_source() {
  cat >/etc/apt/sources.list.d/zabbly-incus-stable.sources <<EOF
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: ${CODENAME}
Components: main
Architectures: ${ARCH}
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF
}

enable_incus_service() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now incus.service >/dev/null 2>&1 || systemctl restart incus.service
  fi
}

wait_for_incus() {
  local sockets=(
    /run/incus/unix.socket
    /var/lib/incus/unix.socket
  )

  local attempt socket
  for attempt in $(seq 1 60); do
    for socket in "${sockets[@]}"; do
      if [[ -S "$socket" ]]; then
        return 0
      fi
    done

    sleep 1
  done

  printf 'timed out waiting for the Incus unix socket\n' >&2
  return 1
}

configure_firewall() {
  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow 8443/tcp >/dev/null 2>&1 || true
  fi
}

add_target_user_to_incus_admin() {
  if [[ -n "$TARGET_USER" && "$TARGET_USER" != "root" ]] && getent group incus-admin >/dev/null 2>&1; then
    usermod -aG incus-admin "$TARGET_USER" || true
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode=*)
      MODE="${1#*=}"
      shift
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  echo "this installer must run as root" >&2
  exit 1
fi

if [[ ! -f /etc/os-release ]]; then
  echo "expected /etc/os-release on this Linux host" >&2
  exit 1
fi

# shellcheck disable=SC1091
source /etc/os-release

if [[ "${ID:-}" != "debian" && "${ID:-}" != "ubuntu" ]]; then
  echo "this installer currently supports Debian and Ubuntu only" >&2
  exit 1
fi

ARCH="$(dpkg --print-architecture)"
CODENAME="${VERSION_CODENAME:-}"
if [[ -z "$CODENAME" ]]; then
  echo "could not determine the distro codename from /etc/os-release" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
LOG_DIR="$(mktemp -d)"

printf 'Capsule Incus installer\n'

run_step "Refreshing apt package indexes" apt-get update
run_step "Installing base packages" apt-get install -y ca-certificates curl gpg
run_step "Installing the Incus signing key" install_repository_key
run_step "Writing the Incus apt source" write_repository_source

run_step "Refreshing apt package indexes for Incus" apt-get update

PACKAGE="incus"
if [[ "$MODE" == "client" ]]; then
  PACKAGE="incus-client"
fi

run_step "Installing ${PACKAGE}" apt-get install -y "${PACKAGE}"

if [[ "$MODE" != "server" ]]; then
  exit 0
fi

run_step "Installing uidmap helpers" apt-get install -y uidmap

TARGET_USER="${SUDO_USER:-${USER:-root}}"
run_step "Starting the Incus service" enable_incus_service
run_step "Waiting for the Incus socket" wait_for_incus
run_step "Opening the Incus API port in ufw" configure_firewall
run_step "Adding ${TARGET_USER} to the incus-admin group" add_target_user_to_incus_admin
