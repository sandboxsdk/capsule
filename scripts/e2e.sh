#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${CAPSULE_E2E_STATE_DIR:-${TMPDIR:-/tmp}/capsule-e2e-local}"
RELEASE_DIR="$STATE_DIR/release"
BUILD_DIR="$STATE_DIR/build"
SSH_DIR="$STATE_DIR/ssh"

CLIENT_NAME="${CAPSULE_E2E_CLIENT_NAME:-capsule-client}"
SERVER_NAME="${CAPSULE_E2E_SERVER_NAME:-capsule-server}"
NETWORK_NAME="${CAPSULE_E2E_NETWORK_NAME:-capsule-e2e}"
CLIENT_IMAGE="${CAPSULE_E2E_CLIENT_IMAGE:-capsule-e2e-client:local}"
SERVER_IMAGE="${CAPSULE_E2E_SERVER_IMAGE:-capsule-e2e-server:local}"
CONTAINER_RUNTIME=""

CLIENT_HTTP_PORT="${CAPSULE_E2E_CLIENT_HTTP_PORT:-18080}"
VERIFY_CONTAINER_NAME="${CAPSULE_E2E_VERIFY_CONTAINER_NAME:-capsule-e2e-alpine}"
DEFAULT_VERSION="v0.0.0-e2e"
SPINNER_FRAMES=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')
COLOR_CYAN=$'\033[36m'
COLOR_GREEN=$'\033[32m'
COLOR_RED=$'\033[31m'
COLOR_RESET=$'\033[0m'

usage() {
  cat <<'EOF'
Usage: scripts/e2e.sh [run|ci|up|install|verify|down]

Commands:
  run      Build artifacts, start the container environment, launch the installer, then verify Incus.
  ci       Build artifacts, run the installer with default remote answers, then verify Incus.
  up       Build artifacts and start the container environment.
  install  Run the interactive install script inside capsule-client.
  verify   Launch an Alpine container through Capsule's Incus wrapper from capsule-client and run incus ls on capsule-server.
  down     Remove the containers and network created by this script.

Environment overrides:
  CAPSULE_E2E_CONTAINER_RUNTIME    Container runtime binary to use (docker or podman; default: auto-detect)
  CAPSULE_E2E_STATE_DIR            Persistent temp directory (default: /tmp/capsule-e2e-local)
  CAPSULE_E2E_CLIENT_NAME          Container name for the client (default: capsule-client)
  CAPSULE_E2E_SERVER_NAME          Container name for the server (default: capsule-server)
  CAPSULE_E2E_NETWORK_NAME         Container network name (default: capsule-e2e)
  CAPSULE_E2E_CLIENT_IMAGE         Container image tag for the client image
  CAPSULE_E2E_SERVER_IMAGE         Container image tag for the server image
  CAPSULE_E2E_CLIENT_HTTP_PORT     Local HTTP port inside capsule-client used for the release asset
  CAPSULE_E2E_VERIFY_CONTAINER_NAME Incus instance name used during verification
  CAPSULE_E2E_SETUP_INPUT          Non-interactive setup answers with \\n escapes (default: option 2 + root@capsule-server)
EOF
}

log() {
  printf '[capsule-e2e] %s\n' "$*"
}

fail() {
  printf '[capsule-e2e] %s\n' "$*" >&2
  exit 1
}

writer_is_terminal() {
  [[ -t 1 ]]
}

run_step() {
  local label="$1"
  shift

  local log_dir="$STATE_DIR/logs"
  local log_file="$log_dir/$(printf '%s' "$label" | tr ' /' '__').log"
  mkdir -p "$log_dir"

  if ! writer_is_terminal; then
    printf '%s...\n' "$label"
    if "$@" >"$log_file" 2>&1; then
      printf '%s✓%s %s\n' "$COLOR_GREEN" "$COLOR_RESET" "$label"
      return 0
    fi

    printf '%s✗%s %s\n' "$COLOR_RED" "$COLOR_RESET" "$label" >&2
    cat "$log_file" >&2
    return 1
  fi

  "$@" >"$log_file" 2>&1 &
  local pid=$!
  local frame_index=0

  while kill -0 "$pid" 2>/dev/null; do
    printf '\r%s%s%s %s' "$COLOR_CYAN" "${SPINNER_FRAMES[$frame_index]}" "$COLOR_RESET" "$label"
    frame_index=$(( (frame_index + 1) % ${#SPINNER_FRAMES[@]} ))
    sleep 0.1
  done

  local status
  set +e
  wait "$pid"
  status=$?
  set -e

  printf '\r\033[K'
  if [[ $status -eq 0 ]]; then
    printf '%s✓%s %s\n' "$COLOR_GREEN" "$COLOR_RESET" "$label"
    return 0
  fi

  printf '%s✗%s %s\n' "$COLOR_RED" "$COLOR_RESET" "$label"
  cat "$log_file" >&2
  return "$status"
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

container_runtime_kind() {
  local runtime="${1:-$CONTAINER_RUNTIME}"

  case "$(basename -- "$runtime")" in
    docker) printf 'docker\n' ;;
    podman) printf 'podman\n' ;;
    *) fail "unsupported container runtime: $runtime (expected docker or podman)" ;;
  esac
}

detect_container_runtime() {
  local runtime="${CAPSULE_E2E_CONTAINER_RUNTIME:-}"

  if [[ -n "$runtime" ]]; then
    need_cmd "$runtime"
    container_runtime_kind "$runtime" >/dev/null
    printf '%s\n' "$runtime"
    return
  fi

  if command -v docker >/dev/null 2>&1; then
    printf 'docker\n'
    return
  fi

  if command -v podman >/dev/null 2>&1; then
    printf 'podman\n'
    return
  fi

  fail "missing required container runtime: docker or podman"
}

container_cmd() {
  "$CONTAINER_RUNTIME" "$@"
}

ensure_container_runtime() {
  if [[ -n "${CONTAINER_RUNTIME:-}" ]]; then
    return
  fi

  CONTAINER_RUNTIME="$(detect_container_runtime)"
  log "using container runtime: $CONTAINER_RUNTIME"
}

container_exists() {
  container_cmd ps -a --format '{{.Names}}' | grep -Fxq "$1"
}

container_running() {
  container_cmd ps --format '{{.Names}}' | grep -Fxq "$1"
}

container_network_exists() {
  container_cmd network ls --format '{{.Name}}' | grep -Fxq "$1"
}

normalize_arch() {
  case "$1" in
    amd64|x86_64) printf 'amd64\n' ;;
    arm64|aarch64) printf 'arm64\n' ;;
    *) fail "unsupported container runtime architecture: $1" ;;
  esac
}

sha256_write() {
  local source="$1"
  local output="$2"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$source" | awk '{print $1}' >"$output"
    return
  fi

  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$source" | awk '{print $1}' >"$output"
    return
  fi

  fail "missing sha256sum or shasum"
}

container_arch() {
  case "$(container_runtime_kind)" in
    docker)
      container_cmd info --format '{{.Architecture}}'
      ;;
    podman)
      container_cmd info --format '{{.Host.Arch}}'
      ;;
  esac
}

target_arch() {
  normalize_arch "$(container_arch)"
}

release_version() {
  local git_suffix=""
  if git -C "$ROOT_DIR" rev-parse --short HEAD >/dev/null 2>&1; then
    git_suffix="$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
  fi

  if [[ -n "$git_suffix" ]]; then
    printf '%s-%s\n' "$DEFAULT_VERSION" "$git_suffix"
    return
  fi

  printf '%s\n' "$DEFAULT_VERSION"
}

release_archive_name() {
  printf 'capsule_%s_linux_%s.tar.gz\n' "$(release_version)" "$(target_arch)"
}

ensure_state_dir() {
  mkdir -p "$STATE_DIR" "$RELEASE_DIR" "$BUILD_DIR" "$SSH_DIR"
}

clean_state_dir() {
  rm -rf "$RELEASE_DIR" "$BUILD_DIR" "$SSH_DIR"
  ensure_state_dir
}

build_release() {
  local arch version archive

  arch="$(target_arch)"
  version="$(release_version)"
  archive="$(release_archive_name)"

  log "building Linux release artifact for $arch"
  mkdir -p "$RELEASE_DIR" "$BUILD_DIR"
  rm -f "$BUILD_DIR/capsule" "$RELEASE_DIR/$archive" "$RELEASE_DIR/$archive.sha256"

  (
    cd "$ROOT_DIR"
    GOOS=linux GOARCH="$arch" CGO_ENABLED=0 \
      go build \
      -ldflags "-X github.com/sandboxsdk/capsule/internal/version.Version=$version" \
      -o "$BUILD_DIR/capsule" \
      ./cmd/capsule
  )

  tar -czf "$RELEASE_DIR/$archive" -C "$BUILD_DIR" capsule
  sha256_write "$RELEASE_DIR/$archive" "$RELEASE_DIR/$archive.sha256"
}

build_client_image() {
  log "building $(container_runtime_kind) image $CLIENT_IMAGE"
  container_cmd build \
    --tag "$CLIENT_IMAGE" \
    --file - \
    "$ROOT_DIR" <<'EOF'
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    openssh-client \
    procps \
    python3 \
    sudo \
    tar \
  && rm -rf /var/lib/apt/lists/*

CMD ["sleep", "infinity"]
EOF
}

build_server_image() {
  local runtime_kind
  runtime_kind="$(container_runtime_kind)"

  log "building $runtime_kind image $SERVER_IMAGE"
  container_cmd build \
    --tag "$SERVER_IMAGE" \
    --file - \
    "$ROOT_DIR" <<EOF
FROM ubuntu:24.04

ENV container=$runtime_kind
ENV DEBIAN_FRONTEND=noninteractive

STOPSIGNAL SIGRTMIN+3

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    dbus \
    gnupg \
    openssh-server \
    python3 \
    sudo \
    systemd \
    systemd-sysv \
  && rm -rf /var/lib/apt/lists/* \
  && mkdir -p /run/sshd

CMD ["/sbin/init"]
EOF
}

wait_for_systemd() {
  local deadline status
  deadline=$((SECONDS + 60))

  while (( SECONDS < deadline )); do
    status="$(container_cmd exec "$SERVER_NAME" systemctl is-system-running 2>/dev/null || true)"
    if [[ "$status" == "running" || "$status" == "degraded" ]]; then
      return 0
    fi
    sleep 1
  done

  container_cmd exec "$SERVER_NAME" systemctl --no-pager status || true
  fail "timed out waiting for systemd in $SERVER_NAME"
}

create_ssh_keypair() {
  if [[ -f "$SSH_DIR/id_ed25519" && -f "$SSH_DIR/id_ed25519.pub" ]]; then
    return
  fi

  rm -f "$SSH_DIR/id_ed25519" "$SSH_DIR/id_ed25519.pub"
  ssh-keygen -q -t ed25519 -N '' -f "$SSH_DIR/id_ed25519" -C "capsule-e2e"
}

configure_server_ssh() {
  log "configuring SSH access on $SERVER_NAME"
  container_cmd exec "$SERVER_NAME" bash -lc "install -d -m 700 /root/.ssh"
  container_cmd cp "$SSH_DIR/id_ed25519.pub" "$SERVER_NAME:/root/.ssh/authorized_keys" >/dev/null
  container_cmd exec "$SERVER_NAME" bash -lc "chown root:root /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys"
  container_cmd exec "$SERVER_NAME" bash -lc "cat >/etc/ssh/sshd_config.d/10-capsule-e2e.conf <<'EOF'
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication no
EOF"
  container_cmd exec "$SERVER_NAME" systemctl restart ssh
}

configure_client_ssh() {
  log "configuring SSH client on $CLIENT_NAME"
  container_cmd exec "$CLIENT_NAME" bash -lc "install -d -m 700 /root/.ssh"
  container_cmd cp "$SSH_DIR/id_ed25519" "$CLIENT_NAME:/root/.ssh/id_ed25519" >/dev/null
  container_cmd exec "$CLIENT_NAME" bash -lc "chown root:root /root/.ssh/id_ed25519 && chmod 600 /root/.ssh/id_ed25519"
  container_cmd exec "$CLIENT_NAME" bash -lc "touch /root/.ssh/known_hosts && chmod 600 /root/.ssh/known_hosts"
  container_cmd exec "$CLIENT_NAME" bash -lc "ssh-keyscan -H $SERVER_NAME >> /root/.ssh/known_hosts 2>/dev/null"
  container_cmd exec "$CLIENT_NAME" bash -lc "cat >/root/.ssh/config <<'EOF'
Host $SERVER_NAME
  HostName $SERVER_NAME
  User root
  IdentityFile /root/.ssh/id_ed25519
  StrictHostKeyChecking accept-new
  UserKnownHostsFile /root/.ssh/known_hosts
EOF
chmod 600 /root/.ssh/config"
}

assert_ssh_connectivity() {
  log "verifying SSH connectivity from $CLIENT_NAME to $SERVER_NAME"
  local deadline=$((SECONDS + 20))
  while (( SECONDS < deadline )); do
    if container_cmd exec "$CLIENT_NAME" ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new "root@$SERVER_NAME" 'printf ok' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  container_cmd exec "$CLIENT_NAME" ssh -v -o BatchMode=yes -o StrictHostKeyChecking=accept-new "root@$SERVER_NAME" 'printf ok' || true
  fail "SSH from $CLIENT_NAME to $SERVER_NAME did not become ready"
}

ensure_release_http_server() {
  local server_pattern http_pid
  server_pattern="[p]ython3 -m http.server $CLIENT_HTTP_PORT"
  http_pid="$(container_cmd exec "$CLIENT_NAME" bash -lc "pgrep -f \"$server_pattern\" | head -n 1 || true")"
  if [[ -n "$http_pid" ]]; then
    return 0
  fi

  log "starting local release HTTP server inside $CLIENT_NAME"
  container_cmd exec "$CLIENT_NAME" bash -lc "pkill -f \"$server_pattern\" >/dev/null 2>&1 || true"
  container_cmd exec -d "$CLIENT_NAME" bash -lc "cd /opt/capsule-e2e/release && exec python3 -m http.server $CLIENT_HTTP_PORT --bind 127.0.0.1 >/tmp/capsule-release-http.log 2>&1"

  local deadline=$((SECONDS + 20))
  while (( SECONDS < deadline )); do
    if container_cmd exec "$CLIENT_NAME" curl -fsSI "http://127.0.0.1:$CLIENT_HTTP_PORT/$(release_archive_name)" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  container_cmd exec "$CLIENT_NAME" bash -lc "cat /tmp/capsule-release-http.log" || true
  fail "the local release HTTP server inside $CLIENT_NAME did not become ready"
}

start_network() {
  if container_network_exists "$NETWORK_NAME"; then
    return
  fi

  log "creating $(container_runtime_kind) network $NETWORK_NAME"
  container_cmd network create "$NETWORK_NAME" >/dev/null
}

remove_existing_container() {
  if container_exists "$1"; then
    log "removing existing container $1"
    container_cmd rm -f "$1" >/dev/null
  fi
}

start_server_container() {
  remove_existing_container "$SERVER_NAME"

  log "starting $SERVER_NAME"
  container_cmd run -d \
    --name "$SERVER_NAME" \
    --hostname "$SERVER_NAME" \
    --network "$NETWORK_NAME" \
    --privileged \
    --cgroupns=host \
    --tmpfs /tmp:exec \
    --tmpfs /run \
    --tmpfs /run/lock \
    --volume /sys/fs/cgroup:/sys/fs/cgroup:rw \
    "$SERVER_IMAGE" >/dev/null

  wait_for_systemd
}

start_client_container() {
  remove_existing_container "$CLIENT_NAME"

  log "starting $CLIENT_NAME"
  container_cmd run -d \
    --name "$CLIENT_NAME" \
    --hostname "$CLIENT_NAME" \
    --network "$NETWORK_NAME" \
    --volume "$ROOT_DIR:/workspace:ro" \
    --volume "$RELEASE_DIR:/opt/capsule-e2e/release:ro" \
    "$CLIENT_IMAGE" >/dev/null
}

setup_server() {
  build_server_image
  start_network
  start_server_container
  configure_server_ssh
}

setup_client() {
  build_client_image
  start_client_container
  configure_client_ssh
  assert_ssh_connectivity
  ensure_release_http_server
}

up() {
  local arch

  ensure_state_dir
  clean_state_dir
  create_ssh_keypair
  arch="$(target_arch)"

  run_step "Build Linux $arch" build_release
  run_step "Setting up server" setup_server
  run_step "Setting up client" setup_client
}

install() {
  local version

  container_running "$CLIENT_NAME" || fail "$CLIENT_NAME is not running; run '$0 up' first"
  container_running "$SERVER_NAME" || fail "$SERVER_NAME is not running; run '$0 up' first"
  [[ -t 0 && -t 1 ]] || fail "interactive install requires a real terminal"

  version="$(release_version)"
  ensure_release_http_server

  printf '\n'
  printf 'Connect to the server using:\n'
  printf '  ssh root@%s\n\n' "$SERVER_NAME"

  container_cmd exec -it \
    -e CAPSULE_INSTALL_URL="http://127.0.0.1:$CLIENT_HTTP_PORT" \
    -e CAPSULE_INSTALL_VERSION="$version" \
    -e CAPSULE_INSTALL_BIN_DIR="/usr/local/bin" \
    "$CLIENT_NAME" \
    bash -lc "cd /workspace && ./scripts/install.sh"
}

noninteractive_setup_input() {
  if [[ -n "${CAPSULE_E2E_SETUP_INPUT:-}" ]]; then
    printf '%b' "$CAPSULE_E2E_SETUP_INPUT"
    return
  fi

  printf '2\nroot@%s\n' "$SERVER_NAME"
}

install_ci() {
  local version

  container_running "$CLIENT_NAME" || fail "$CLIENT_NAME is not running; run '$0 up' first"
  container_running "$SERVER_NAME" || fail "$SERVER_NAME is not running; run '$0 up' first"

  version="$(release_version)"
  ensure_release_http_server

  printf '\n'
  printf 'Connect to the server using:\n'
  printf '  ssh root@%s\n\n' "$SERVER_NAME"

  noninteractive_setup_input | container_cmd exec -i \
    -e CAPSULE_INSTALL_URL="http://127.0.0.1:$CLIENT_HTTP_PORT" \
    -e CAPSULE_INSTALL_VERSION="$version" \
    -e CAPSULE_INSTALL_BIN_DIR="/usr/local/bin" \
    -e CAPSULE_INSTALL_NO_TTY="1" \
    "$CLIENT_NAME" \
    bash -lc "cd /workspace && ./scripts/install.sh"
}

verify() {
  container_running "$CLIENT_NAME" || fail "$CLIENT_NAME is not running; run '$0 up' first"
  container_running "$SERVER_NAME" || fail "$SERVER_NAME is not running; run '$0 up' first"

  log "launching a lightweight Alpine instance via the configured Capsule Incus remote"
  container_cmd exec "$CLIENT_NAME" bash -lc "capsule incus delete -f $VERIFY_CONTAINER_NAME >/dev/null 2>&1 || true"
  container_cmd exec "$CLIENT_NAME" bash -lc '
set -euo pipefail
name="'"$VERIFY_CONTAINER_NAME"'"
launch_output=""

launch_instance() {
  local image="$1"
  shift

  local status
  set +e
  launch_output="$(capsule incus launch "$image" "$name" "$@" 2>&1)"
  status=$?
  set -e

  return "$status"
}

for image in images:alpine/3.20 images:alpine/3.19 images:alpine/edge; do
  if launch_instance "$image"; then
    printf "%s\n" "$launch_output"
    exit 0
  fi

  printf "%s\n" "$launch_output" >&2
  if [[ "$launch_output" == *idmap* ]]; then
    echo "Retrying with security.privileged=true because nested idmap delegation is unavailable" >&2
    capsule incus delete -f "$name" >/dev/null 2>&1 || true
    if launch_instance "$image" -c security.privileged=true; then
      printf "%s\n" "$launch_output"
      exit 0
    fi
  fi

  capsule incus delete -f "$name" >/dev/null 2>&1 || true
done
echo "unable to launch an Alpine image from the default images remote" >&2
exit 1
'

  log "Incus instances visible on $SERVER_NAME"
  container_cmd exec "$SERVER_NAME" incus ls
}

down() {
  remove_existing_container "$CLIENT_NAME"
  remove_existing_container "$SERVER_NAME"

  if container_network_exists "$NETWORK_NAME"; then
    log "removing $(container_runtime_kind) network $NETWORK_NAME"
    container_cmd network rm "$NETWORK_NAME" >/dev/null
  fi
}

require_build_prereqs() {
  ensure_container_runtime
  need_cmd go
  need_cmd tar
  need_cmd ssh-keygen
}

main() {
  local command="${1:-run}"

  case "$command" in
    run)
      require_build_prereqs
      up
      install
      verify
      ;;
    ci)
      require_build_prereqs
      up
      install_ci
      verify
      ;;
    up)
      require_build_prereqs
      up
      ;;
    install)
      ensure_container_runtime
      install
      ;;
    verify)
      ensure_container_runtime
      verify
      ;;
    down)
      ensure_container_runtime
      down
      ;;
    help|-h|--help)
      usage
      ;;
    *)
      usage
      fail "unknown command: $command"
      ;;
  esac
}

main "$@"
