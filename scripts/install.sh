#!/usr/bin/env sh

BIN_DIR="${CAPSULE_INSTALL_BIN_DIR:-}"
REPO="${CAPSULE_INSTALL_REPO:-MSch/capsule}"
VERSION="${CAPSULE_INSTALL_VERSION:-}"
BASE_URL="${CAPSULE_INSTALL_URL:-}"
ASSET_PREFIX="${CAPSULE_INSTALL_ASSET_PREFIX:-capsule}"
ARCHIVE_NAME_OVERRIDE="${CAPSULE_INSTALL_ARCHIVE_NAME:-}"
CHECKSUM_NAME_OVERRIDE="${CAPSULE_INSTALL_CHECKSUM_NAME:-}"
BIN_NAME="${CAPSULE_INSTALL_BIN_NAME:-capsule}"

SPINNER_CHARS='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
SPINNER_PID=""
SPINNER_MSG=""
TMPDIR_INSTALL=""

CYAN='\033[36m'
GREEN='\033[32m'
RED='\033[31m'
YELLOW='\033[33m'
RESET='\033[0m'

cleanup() {
  stop_spinner
  if [ -n "${TMPDIR_INSTALL:-}" ] && [ -d "$TMPDIR_INSTALL" ]; then
    rm -rf "$TMPDIR_INSTALL"
  fi
}
trap cleanup EXIT INT HUP TERM

start_spinner() {
  SPINNER_MSG="$1"
  if [ ! -t 1 ]; then
    return
  fi

  (
    i=0
    while true; do
      printf "\r${CYAN}%s${RESET} %s " \
        "$(printf "%s" "$SPINNER_CHARS" | cut -c $(( i + 1 )))" \
        "$SPINNER_MSG"
      sleep 0.1
      i=$(( (i + 1) % ${#SPINNER_CHARS} ))
    done
  ) &
  SPINNER_PID=$!
}

stop_spinner() {
  if [ -n "${SPINNER_PID:-}" ]; then
    kill "$SPINNER_PID" 2>/dev/null || true
    wait "$SPINNER_PID" 2>/dev/null || true
    SPINNER_PID=""
    if [ -t 1 ]; then
      printf "\r\033[K"
    fi
  fi
}

step_done() {
  msg="$1"
  stop_spinner
  printf "${GREEN}✓${RESET} %s\n" "$msg"
}

step_fail() {
  msg="$1"
  stop_spinner
  printf "${RED}✗${RESET} %s\n" "$msg" >&2
}

usage() {
  cat <<EOF
Install the Capsule CLI

Usage: $0 [--bin-dir <dir>]

Options:
  --bin-dir    Directory to install the capsule binary into (default: ~/.local/bin).
  -h, --help   Show this help message.

Environment variables:
  CAPSULE_INSTALL_REPO          Override the GitHub repo (default: MSch/capsule)
  CAPSULE_INSTALL_VERSION       Override the release tag instead of resolving latest
  CAPSULE_INSTALL_URL           Override the base download URL
  CAPSULE_INSTALL_ASSET_PREFIX  Override the asset name prefix (default: capsule)
  CAPSULE_INSTALL_ARCHIVE_NAME  Override the full archive filename
  CAPSULE_INSTALL_CHECKSUM_NAME Override the full checksum filename
  CAPSULE_INSTALL_BIN_NAME      Override the binary name inside the archive
  CAPSULE_INSTALL_BIN_DIR       Override the install directory
EOF
}

err() {
  echo "Error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || err "Required command not found: $1"
}

need_http_cmd() {
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    err "Neither curl nor wget found. Please install one of them."
  fi
}

http_download() {
  url="$1"
  output="$2"

  if command -v curl >/dev/null 2>&1; then
    HTTP_CODE=$(curl -sSL -w '%{http_code}' -o "$output" "$url" 2>/dev/null) || HTTP_CODE="000"
  elif command -v wget >/dev/null 2>&1; then
    stderr_file=$(mktemp)
    if wget -q --server-response -O "$output" "$url" 2>"$stderr_file"; then
      HTTP_CODE="200"
    else
      HTTP_CODE=$(grep -o 'HTTP/[0-9.]* [0-9]*' "$stderr_file" | tail -1 | awk '{print $2}')
      if [ -z "$HTTP_CODE" ]; then
        HTTP_CODE="000"
      fi
    fi
    rm -f "$stderr_file"
  fi
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --bin-dir)
        [ -n "${2:-}" ] || err "Missing value for --bin-dir"
        BIN_DIR="$2"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        err "Unknown argument: $1"
        ;;
    esac
  done
}

detect_platform() {
  os=$(uname -s)
  arch=$(uname -m)

  case "$arch" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) err "Unsupported architecture: $arch" ;;
  esac

  case "$os" in
    Linux) PLATFORM="linux" ;;
    Darwin) PLATFORM="darwin" ;;
    *) err "Unsupported OS: $os (use a native installer on Windows)" ;;
  esac

  EXT="tar.gz"
}

resolve_version() {
  if [ -n "$VERSION" ] || [ -n "$ARCHIVE_NAME_OVERRIDE" ]; then
    return
  fi

  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  meta_tmp=$(mktemp)

  start_spinner "Fetching latest release"
  http_download "$api_url" "$meta_tmp"

  if [ "$HTTP_CODE" != "200" ]; then
    rm -f "$meta_tmp" || true
    step_fail "Fetching latest release"
    if [ "$HTTP_CODE" = "000" ]; then
      err "Network error: could not connect to api.github.com"
    elif [ "$HTTP_CODE" = "404" ]; then
      err "No GitHub release found for ${REPO} yet."
    elif [ "$HTTP_CODE" = "403" ]; then
      err "GitHub API rate limit exceeded while resolving the latest release."
    else
      err "Failed to resolve the latest release (HTTP $HTTP_CODE)"
    fi
  fi

  VERSION=$(sed -n 's/^[[:space:]]*\"tag_name\":[[:space:]]*\"\([^\"]*\)\".*/\1/p' "$meta_tmp" | head -n 1)
  rm -f "$meta_tmp" || true

  if [ -z "$VERSION" ]; then
    step_fail "Fetching latest release"
    err "Could not determine the latest release tag for ${REPO}."
  fi

  step_done "Fetching latest release ($VERSION)"
}

dir_in_path() {
  check_dir="$1"
  if [ -d "$check_dir" ]; then
    check_dir=$(cd "$check_dir" 2>/dev/null && pwd) || return 1
  fi
  echo ":$PATH:" | grep -q ":$check_dir:"
}

choose_bindir() {
  default_bin_dir="${CAPSULE_INSTALL_DEFAULT_BIN_DIR:-$HOME/.local/bin}"

  if [ -n "$BIN_DIR" ]; then
    DEST_DIR="$BIN_DIR"
  else
    DEST_DIR=""
    old_ifs=$IFS
    IFS=:
    for dir in ${CAPSULE_INSTALL_PREFERRED_DIRS:-$HOME/.local/bin:$HOME/bin:$HOME/.bin}; do
      if dir_in_path "$dir"; then
        DEST_DIR="$dir"
        break
      fi
    done
    IFS=$old_ifs

    if [ -z "$DEST_DIR" ]; then
      printf "${YELLOW}Warning:${RESET} None of the standard bin directories (~/.local/bin, ~/bin, ~/.bin) are in your PATH.\n"
      printf "         Using ${CYAN}%s${RESET} - you may need to add it to your PATH.\n" "$default_bin_dir"
      DEST_DIR="$default_bin_dir"
    fi
  fi

  if ! mkdir_err=$(mkdir -p "$DEST_DIR" 2>&1); then
    err "Could not create bin directory $DEST_DIR: $mkdir_err"
  fi
  if [ ! -d "$DEST_DIR" ]; then
    err "Could not create bin directory: $DEST_DIR"
  fi
}

verify_checksum() {
  archive_path="$1"
  sha_url="$2"

  start_spinner "Verifying checksum"

  sha_tmp=$(mktemp)
  http_download "$sha_url" "$sha_tmp"

  if [ "$HTTP_CODE" != "200" ]; then
    rm -f "$sha_tmp" || true
    stop_spinner
    printf "${YELLOW}⚠${RESET} Checksum file not available, skipping verification\n"
    return
  fi

  expected=$(awk 'NR==1 {print $1}' "$sha_tmp")
  rm -f "$sha_tmp" || true

  if [ -z "$expected" ]; then
    step_fail "Verifying checksum"
    err "Checksum file was empty: $sha_url"
  fi

  if command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$archive_path" | awk '{print $1}')
  else
    need_cmd sha256sum
    actual=$(sha256sum "$archive_path" | awk '{print $1}')
  fi

  if [ "$actual" != "$expected" ]; then
    step_fail "Verifying checksum"
    err "Checksum mismatch: expected $expected, got $actual. The download may be corrupted."
  fi

  step_done "Verifying checksum"
}

extract_and_install() {
  archive_path="$1"
  extract_dir="$2/extract"

  start_spinner "Installing to $DEST_DIR"

  if ! mkdir_err=$(mkdir -p "$extract_dir" 2>&1); then
    step_fail "Installing to $DEST_DIR"
    err "Could not create extract directory: $mkdir_err"
  fi

  if ! extract_err=$(tar -xzf "$archive_path" -C "$extract_dir" 2>&1); then
    step_fail "Installing to $DEST_DIR"
    err "Failed to extract archive: $extract_err"
  fi

  binary_path=$(find "$extract_dir" -type f -name "$BIN_NAME" | head -n 1)
  if [ -z "$binary_path" ]; then
    step_fail "Installing to $DEST_DIR"
    err "Could not find $BIN_NAME inside the downloaded archive."
  fi

  if ! install_err=$(install -m 0755 "$binary_path" "$DEST_DIR/$BIN_NAME" 2>&1); then
    step_fail "Installing to $DEST_DIR"
    err "Failed to install binary to $DEST_DIR: $install_err"
  fi

  step_done "Installed to $DEST_DIR"
}

post_install_note() {
  if ! echo ":$PATH:" | grep -q ":$DEST_DIR:"; then
    printf "  Add to PATH: export PATH=\"%s:\$PATH\"\n" "$DEST_DIR"
  fi

  printf "\n💊 Starting ${CYAN}%s${RESET}...\n" "$BIN_NAME setup"
}

run_setup() {
  setup_bin="$DEST_DIR/$BIN_NAME"

  if [ ! -x "$setup_bin" ]; then
    err "Installed binary is not executable: $setup_bin"
  fi

  if [ -r /dev/tty ] && [ -w /dev/tty ]; then
    exec "$setup_bin" setup </dev/tty >/dev/tty 2>/dev/tty
  fi

  exec "$setup_bin" setup
}

main() {
  parse_args "$@"
  detect_platform

  need_http_cmd
  need_cmd tar
  need_cmd install

  resolve_version
  choose_bindir

  if [ -n "$BASE_URL" ]; then
    DOWNLOAD_BASE_URL="$BASE_URL"
  elif [ -n "$VERSION" ]; then
    DOWNLOAD_BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
  else
    DOWNLOAD_BASE_URL="https://github.com/${REPO}/releases/latest/download"
  fi

  ARCHIVE_NAME="${ARCHIVE_NAME_OVERRIDE:-${ASSET_PREFIX}_${VERSION}_${PLATFORM}_${ARCH}.${EXT}}"
  CHECKSUM_NAME="${CHECKSUM_NAME_OVERRIDE:-${ARCHIVE_NAME}.sha256}"
  DOWNLOAD_URL="${DOWNLOAD_BASE_URL}/${ARCHIVE_NAME}"
  CHECKSUM_URL="${DOWNLOAD_BASE_URL}/${CHECKSUM_NAME}"

  TMPDIR_INSTALL=$(mktemp -d)
  ARCHIVE_PATH="$TMPDIR_INSTALL/$ARCHIVE_NAME"

  start_spinner "Downloading Capsule CLI"
  http_download "$DOWNLOAD_URL" "$ARCHIVE_PATH"

  if [ "$HTTP_CODE" != "200" ]; then
    step_fail "Downloading Capsule CLI"
    if [ "$HTTP_CODE" = "000" ]; then
      err "Network error: could not connect to $DOWNLOAD_BASE_URL"
    elif [ "$HTTP_CODE" = "404" ]; then
      err "Release asset not found: $ARCHIVE_NAME. Publish it under https://github.com/${REPO}/releases or override CAPSULE_INSTALL_ARCHIVE_NAME."
    else
      err "Download failed (HTTP $HTTP_CODE)"
    fi
  fi

  step_done "Downloading Capsule CLI"
  verify_checksum "$ARCHIVE_PATH" "$CHECKSUM_URL"
  extract_and_install "$ARCHIVE_PATH" "$TMPDIR_INSTALL"
  post_install_note
  run_setup
}

main "$@"
