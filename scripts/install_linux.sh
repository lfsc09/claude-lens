#!/bin/bash
set -e

SERVICE_NAME="claude-lens"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
DOWNLOAD_URL="https://github.com/lfsc09/claude-lens/releases/latest/download/claude-lens-linux-amd64"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"

SCRIPT_NAME="install_linux.sh"
_COLOR_YELLOW=$'\033[33m'
_COLOR_RED=$'\033[31m'
_COLOR_RESET=$'\033[0m'

# log prints a message to the terminal, prefixed with the script name.
# level selects the channel and coloring: info (stdout, plain),
# warn (stdout, yellow) or error (stderr, red).
log() {
  local level="$1"
  shift
  local message="$*"
  case "$level" in
    info) printf '%s: %s\n' "$SCRIPT_NAME" "$message" ;;
    warn) printf '%s: %s%s%s\n' "$SCRIPT_NAME" "$_COLOR_YELLOW" "$message" "$_COLOR_RESET" ;;
    error) printf '%s: %s%s%s\n' "$SCRIPT_NAME" "$_COLOR_RED" "$message" "$_COLOR_RESET" >&2 ;;
  esac
}

tmp_files=()

# cleanup removes every temp file registered in tmp_files.
cleanup() {
  rm -f "${tmp_files[@]}"
}
trap cleanup EXIT

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# normalize_port strips an optional leading ":" from value and prints the
# remaining port number, or exits with an error if it isn't one.
normalize_port() {
  local flag="$1" port="${2#:}"
  case "$port" in
    ''|*[!0-9]*)
      log error "${flag} must be a plain port number (e.g. 7801), got '${2}'."
      exit 1
      ;;
  esac
  if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
    log error "${flag} must be between 1 and 65535, got '${2}'."
    exit 1
  fi
  printf '%s' "$port"
}

# saved_value prints the last value stored for key in the env file from a
# previous install. The file is parsed, never sourced: it lives in a
# user-writable directory and this script runs as root.
saved_value() {
  sed -n "s/^$1=\"\(.*\)\"\$/\1/p" "$ENV_FILE" | tail -n 1
}

usage() {
  cat <<'USAGE'
Usage: install_linux.sh [options]

Safe to re-run at any time to update the binary and/or reconfigure the
service. Any option you omit keeps whatever was set on a previous run
(or the default shown, on a first install).

  --proxy-addr PORT         Proxy listen port (default: 7801)
  --admin-addr PORT         Admin listen port (default: 7802)
  --install-dir PATH        Directory holding the binary, data/ and logs/ (default: ~/.claude-lens)
  --as-service              Configure and start claude-lens as a systemd service
                             (default: off - just downloads/updates the binary)
  -h, --help                Show this help
USAGE
}

# ── Parse flags ───────────────────────────────────────────────────────
flag_proxy_addr=""
flag_admin_addr=""
flag_install_dir=""
flag_as_service=""
while [ $# -gt 0 ]; do
  case "$1" in
    --proxy-addr) flag_proxy_addr="$2"; shift 2 ;;
    --proxy-addr=*) flag_proxy_addr="${1#*=}"; shift ;;
    --admin-addr) flag_admin_addr="$2"; shift 2 ;;
    --admin-addr=*) flag_admin_addr="${1#*=}"; shift ;;
    --install-dir) flag_install_dir="$2"; shift 2 ;;
    --install-dir=*) flag_install_dir="${1#*=}"; shift ;;
    --as-service) flag_as_service="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) log error "Unknown option: $1"; usage; exit 1 ;;
  esac
done

if [ "$EUID" -ne 0 ]; then
  log error "Please run as root (e.g., sudo ./install_linux.sh)"
  exit 1
fi

if [ "$(uname -m)" != "x86_64" ]; then
  log error "Unsupported architecture: $(uname -m) (only linux-amd64 builds are published)."
  exit 1
fi

# sudo resets HOME to root's, so resolve the invoking user's real home to
# find their ~/.claude and default install dir, not root's.
target_home="$HOME"
if [ -n "${SUDO_USER:-}" ]; then
  sudo_home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
  target_home="${sudo_home:-$target_home}"
fi

# ── Resolve install/data/log directories ─────────────────────────────────
CLENS_INSTALL_DIR="${flag_install_dir:-${target_home}/.claude-lens}"
case "$CLENS_INSTALL_DIR" in
  "~"|"~/"*) CLENS_INSTALL_DIR="${target_home}${CLENS_INSTALL_DIR#\~}" ;;
esac
case "$CLENS_INSTALL_DIR" in
  /*) ;;
  *) log error "--install-dir must be an absolute path, got '${CLENS_INSTALL_DIR}'."; exit 1 ;;
esac
CLENS_DATA_DIR="${CLENS_INSTALL_DIR}/data"
CLENS_LOG_DIR="${CLENS_INSTALL_DIR}/logs"
ENV_FILE="${CLENS_INSTALL_DIR}/claude-lens.env"

# ── Defaults, then settings carried forward from a previous install ────
saved_proxy_addr=""
saved_admin_addr=""
saved_as_service=""
if [ -f "$ENV_FILE" ]; then
  saved_proxy_addr="$(saved_value CLENS_PROXY_ADDR)"
  saved_admin_addr="$(saved_value CLENS_ADMIN_ADDR)"
  saved_as_service="$(saved_value CLENS_AS_SERVICE)"
fi

# ── Flags override persisted values, which override defaults ───────────
CLENS_PROXY_ADDR="${flag_proxy_addr:-${saved_proxy_addr:-7801}}"
CLENS_ADMIN_ADDR="${flag_admin_addr:-${saved_admin_addr:-7802}}"
CLENS_AS_SERVICE="${flag_as_service:-${saved_as_service:-false}}"

# --proxy-addr/--admin-addr accept a bare port number; normalized here to
# the ":port" form CLENS_PROXY_ADDR/CLENS_ADMIN_ADDR carry at runtime.
CLENS_PROXY_ADDR=":$(normalize_port --proxy-addr "$CLENS_PROXY_ADDR")"
CLENS_ADMIN_ADDR=":$(normalize_port --admin-addr "$CLENS_ADMIN_ADDR")"
if [ "$CLENS_PROXY_ADDR" = "$CLENS_ADMIN_ADDR" ]; then
  log error "--proxy-addr and --admin-addr must differ, both are '${CLENS_PROXY_ADDR#:}'."
  exit 1
fi

# The service runs as the invoking user so it can reach the data and log
# directories under their home.
RUN_USER="${SUDO_USER:-root}"
RUN_GROUP="$(id -gn "$RUN_USER")"

# ── Configure ANTHROPIC_BASE_URL via Claude Code's settings.json ───────
# claude-lens never sees this variable - Claude Code does, by reading the
# `env` block of its own ~/.claude/settings.json. We only touch that file,
# for the port this install's proxy listens on.
proxy_port="${CLENS_PROXY_ADDR##*:}"
expected_anthropic_url="http://localhost:${proxy_port}"

claude_dir="${target_home}/.claude"
settings_file="${claude_dir}/settings.json"

settings_json_snippet=$(cat <<JSON
{
  "env": {
    "ANTHROPIC_BASE_URL": "${expected_anthropic_url}"
  }
}
JSON
)

if [ ! -d "$claude_dir" ]; then
  log error "'${claude_dir}' not found - Claude Code doesn't appear to be installed for this user, or its config lives elsewhere."
  log error "Set ANTHROPIC_BASE_URL yourself in whichever settings.json Claude Code reads, by adding:"
  log error "$settings_json_snippet"
  exit 1
fi

# configure_claude_settings points Claude Code at this install's proxy by
# setting ANTHROPIC_BASE_URL in settings.json, leaving other keys untouched.
configure_claude_settings() {
  if [ ! -f "$settings_file" ]; then
    log info "Creating ${settings_file}..."
    printf '%s\n' "$settings_json_snippet" > "$settings_file"
    chown "${RUN_USER}:${RUN_GROUP}" "$settings_file"
    log info "ANTHROPIC_BASE_URL set to ${expected_anthropic_url} in ${settings_file}."
  else
    if ! command -v jq >/dev/null 2>&1; then
      log error "'${settings_file}' already exists and jq is required to safely read/update it, but jq is not installed."
      log error "Install jq (sudo apt-get install -y jq) and re-run this installer, or add this yourself:"
      log error "$settings_json_snippet"
      exit 1
    fi

    if ! current_url="$(jq -r '.env.ANTHROPIC_BASE_URL // empty' "$settings_file" 2>/dev/null)"; then
      log error "'${settings_file}' exists but isn't valid JSON. Fix it manually, then re-run this installer. It should include:"
      log error "$settings_json_snippet"
      exit 1
    fi
    if [ -z "$current_url" ]; then
      orig_mode="$(stat -c '%a' "$settings_file")"
      orig_owner="$(stat -c '%U:%G' "$settings_file")"
      tmp_settings="$(mktemp)"
      tmp_files+=("$tmp_settings")
      jq --arg url "$expected_anthropic_url" '.env.ANTHROPIC_BASE_URL = $url' "$settings_file" > "$tmp_settings"
      chmod "$orig_mode" "$tmp_settings"
      chown "$orig_owner" "$tmp_settings" 2>/dev/null || true
      mv "$tmp_settings" "$settings_file"
      log info "ANTHROPIC_BASE_URL set to ${expected_anthropic_url} in ${settings_file}."
    elif [ "$current_url" != "$expected_anthropic_url" ]; then
      log error "ANTHROPIC_BASE_URL is already set to '${current_url}' in ${settings_file}, but this install listens at '${expected_anthropic_url}' (from --proxy-addr=${proxy_port})."
      log error "Edit it manually to match, or re-run this installer with --proxy-addr matching the existing value."
      exit 1
    else
      log info "ANTHROPIC_BASE_URL already set to ${expected_anthropic_url} in ${settings_file} - good."
    fi
  fi
}

if [ "$CLENS_AS_SERVICE" = "true" ] && ! command -v systemctl >/dev/null 2>&1; then
  log error "--as-service needs systemd (systemctl not found). Re-run without --as-service to only install the binary."
  exit 1
fi

# The temp files live in the install dir so the final mv is an atomic rename,
# which also replaces a binary that is currently running.
install_dir_existed=false
[ -d "$CLENS_INSTALL_DIR" ] && install_dir_existed=true
mkdir -p "$CLENS_DATA_DIR" "$CLENS_LOG_DIR"
$install_dir_existed || chown "${RUN_USER}:${RUN_GROUP}" "$CLENS_INSTALL_DIR"
chown "${RUN_USER}:${RUN_GROUP}" "$CLENS_DATA_DIR" "$CLENS_LOG_DIR"

log info "Downloading latest ${SERVICE_NAME} binary and checksum..."
tmp_bin="$(mktemp "${CLENS_INSTALL_DIR}/.${SERVICE_NAME}.XXXXXX")"
tmp_sha="$(mktemp)"
tmp_files+=("$tmp_bin" "$tmp_sha")
curl -fsSL "$DOWNLOAD_URL" -o "$tmp_bin"
curl -fsSL "$CHECKSUM_URL" -o "$tmp_sha"

expected_sha="$(awk '{print $1}' "$tmp_sha")"
actual_sha="$(sha256_of "$tmp_bin")"
if [ "$expected_sha" != "$actual_sha" ]; then
  log error "checksum mismatch for downloaded binary (expected ${expected_sha}, got ${actual_sha})."
  log error "Aborting - the existing installation, if any, was left untouched."
  exit 1
fi

chown "${RUN_USER}:${RUN_GROUP}" "$tmp_bin"
chmod 755 "$tmp_bin"
mv "$tmp_bin" "${CLENS_INSTALL_DIR}/${SERVICE_NAME}"

log info "Writing ${ENV_FILE}..."
tmp_env="$(mktemp "${CLENS_INSTALL_DIR}/.${SERVICE_NAME}.env.XXXXXX")"
tmp_files+=("$tmp_env")
cat <<EOF > "$tmp_env"
CLENS_PROXY_ADDR="${CLENS_PROXY_ADDR}"
CLENS_ADMIN_ADDR="${CLENS_ADMIN_ADDR}"
CLENS_DATA_DIR="${CLENS_DATA_DIR}"
CLENS_LOG_DIR="${CLENS_LOG_DIR}"
CLENS_AS_SERVICE="${CLENS_AS_SERVICE}"
EOF
chmod 600 "$tmp_env"
chown "${RUN_USER}:${RUN_GROUP}" "$tmp_env"
mv "$tmp_env" "$ENV_FILE"

if [ "$CLENS_AS_SERVICE" = "true" ]; then
  log info "Creating systemd service unit..."
  cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=Claude Lens Background Service
After=network.target

[Service]
Type=simple
ExecStart=${CLENS_INSTALL_DIR}/${SERVICE_NAME}
EnvironmentFile=-${ENV_FILE}
WorkingDirectory=${CLENS_DATA_DIR}
Restart=always
RestartSec=5
User=${RUN_USER}
Group=${RUN_GROUP}

[Install]
WantedBy=multi-user.target
EOF

  log info "Reloading systemd, enabling, and starting service..."
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"
  sleep 2
  if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    log error "${SERVICE_NAME} did not stay running. Inspect it with: journalctl -u ${SERVICE_NAME} -n 50"
    exit 1
  fi

  log info "${SERVICE_NAME} installed/updated and started as a systemd service!"
  log info "Proxy listening on ${CLENS_PROXY_ADDR}, admin UI on ${CLENS_ADMIN_ADDR}."
  log info "Config saved to ${ENV_FILE} - editing the simple values there and running 'systemctl restart ${SERVICE_NAME}' applies them."
else
  log info "${SERVICE_NAME} binary installed/updated at ${CLENS_INSTALL_DIR}/${SERVICE_NAME}."
  log info "Config saved to ${ENV_FILE}."
  log info "Re-run this installer with --as-service to configure and start it as a systemd service."
fi

configure_claude_settings
