#!/bin/bash
set -e

LABEL="com.user.claude-lens"
PLIST_PATH="$HOME/Library/LaunchAgents/${LABEL}.plist"

SCRIPT_NAME="install_macos.sh"
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

if [ "$EUID" -eq 0 ]; then
  log error "Do not run this installer as root or with sudo: it installs for the current user."
  exit 1
fi

# ── Resolve download URL for this Mac's CPU architecture ────────────────
case "$(uname -m)" in
  arm64) BINARY_ARCH="arm64" ;;
  x86_64) BINARY_ARCH="amd64" ;;
  *)
    log error "Unsupported architecture: $(uname -m)"
    exit 1
    ;;
esac
log info "Detected CPU architecture $(uname -m) - will download the ${BINARY_ARCH} build."
DOWNLOAD_URL="https://github.com/lfsc09/claude-lens/releases/latest/download/claude-lens-darwin-${BINARY_ARCH}"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"

tmp_files=()

# cleanup removes every temp file registered in tmp_files.
cleanup() {
  rm -f "${tmp_files[@]}"
}
trap cleanup EXIT

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
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

# validate_bind_host exits with an error unless value looks like a hostname
# or IP address.
validate_bind_host() {
  case "$1" in
    ''|*[!A-Za-z0-9._:-]*)
      log error "--bind must be a hostname or IP address (e.g. 127.0.0.1), got '${1}'."
      exit 1
      ;;
  esac
}

# listen_addr prints host:port, bracketing IPv6 hosts.
listen_addr() {
  case "$1" in
    *:*) printf '[%s]:%s' "$1" "$2" ;;
    *) printf '%s:%s' "$1" "$2" ;;
  esac
}

# saved_value prints the last value stored for key in the env file from a
# previous install. The file is parsed, never sourced.
saved_value() {
  sed -n "s/^$1=\"\\(.*\\)\"\$/\\1/p" "$ENV_FILE" | tail -n 1
}

usage() {
  cat <<'USAGE'
Usage: install_macos.sh [options]

Safe to re-run at any time to update the binary and/or reconfigure the
service. Any option you omit keeps whatever was set on a previous run
(or the default shown, on a first install).

  --proxy-addr PORT         Proxy listen port (default: 7801)
  --admin-addr PORT         Admin listen port (default: 7802)
  --bind HOST               Interface both servers listen on (default: 127.0.0.1, this
                             machine only). Anything else exposes the admin UI and the
                             proxy, which have no authentication, to your network.
  --install-dir PATH        Directory holding the binary, data/ and logs/ (default: ~/.claude-lens)
  --as-service              Configure and start claude-lens as a launchd agent
                             (default: off - just downloads/updates the binary)
  -h, --help                Show this help
USAGE
}

xml_escape() {
  local s="$1"
  s="${s//&/&amp;}"
  s="${s//</&lt;}"
  s="${s//>/&gt;}"
  printf '%s' "$s"
}

check_install_dir_writable() {
  local dir="$CLENS_INSTALL_DIR"
  while [ ! -d "$dir" ]; do
    dir="$(dirname "$dir")"
  done
  if [ ! -w "$dir" ]; then
    log error "'${CLENS_INSTALL_DIR}' is not writable by $(whoami) (nearest existing directory '${dir}' is not writable)."
    log error "This installer does not use sudo. Fix the permission once, then re-run it:"
    log error "  sudo mkdir -p '${CLENS_INSTALL_DIR}'"
    log error "  sudo chown \"\$(whoami)\" '${CLENS_INSTALL_DIR}'"
    exit 1
  fi
}

# ── Parse flags ───────────────────────────────────────────────────────
flag_proxy_addr=""
flag_admin_addr=""
flag_bind=""
flag_install_dir=""
flag_as_service=""
while [ $# -gt 0 ]; do
  case "$1" in
    --proxy-addr) flag_proxy_addr="$2"; shift 2 ;;
    --proxy-addr=*) flag_proxy_addr="${1#*=}"; shift ;;
    --admin-addr) flag_admin_addr="$2"; shift 2 ;;
    --admin-addr=*) flag_admin_addr="${1#*=}"; shift ;;
    --bind) flag_bind="$2"; shift 2 ;;
    --bind=*) flag_bind="${1#*=}"; shift ;;
    --install-dir) flag_install_dir="$2"; shift 2 ;;
    --install-dir=*) flag_install_dir="${1#*=}"; shift ;;
    --as-service) flag_as_service="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) log error "Unknown option: $1"; usage; exit 1 ;;
  esac
done

# ── Resolve install/data/log directories ─────────────────────────────────
CLENS_INSTALL_DIR="${flag_install_dir:-${HOME}/.claude-lens}"
case "$CLENS_INSTALL_DIR" in
  "~"|"~/"*) CLENS_INSTALL_DIR="${HOME}${CLENS_INSTALL_DIR#\~}" ;;
esac
case "$CLENS_INSTALL_DIR" in
  /*) ;;
  *) log error "--install-dir must be an absolute path, got '${CLENS_INSTALL_DIR}'."; exit 1 ;;
esac
CLENS_DATA_DIR="${CLENS_INSTALL_DIR}/data"
CLENS_LOG_DIR="${CLENS_INSTALL_DIR}/logs"
ENV_FILE="${CLENS_INSTALL_DIR}/claude-lens.env"

# ── Defaults, then settings carried forward from a previous install ────
saved_proxy_port=""
saved_admin_port=""
saved_bind=""
saved_as_service=""
if [ -f "$ENV_FILE" ]; then
  saved_proxy_addr="$(saved_value CLENS_PROXY_ADDR)"
  saved_admin_addr="$(saved_value CLENS_ADMIN_ADDR)"
  saved_as_service="$(saved_value CLENS_AS_SERVICE)"
  saved_proxy_port="${saved_proxy_addr##*:}"
  saved_admin_port="${saved_admin_addr##*:}"
  case "$saved_admin_addr" in
    *:*)
      saved_bind="${saved_admin_addr%:*}"
      saved_bind="${saved_bind#[}"
      saved_bind="${saved_bind%]}"
      ;;
  esac
fi

# ── Flags override persisted values, which override defaults ───────────
proxy_port="$(normalize_port --proxy-addr "${flag_proxy_addr:-${saved_proxy_port:-7801}}")"
admin_port="$(normalize_port --admin-addr "${flag_admin_addr:-${saved_admin_port:-7802}}")"
CLENS_BIND_HOST="${flag_bind:-${saved_bind:-127.0.0.1}}"
CLENS_AS_SERVICE="${flag_as_service:-${saved_as_service:-false}}"

validate_bind_host "$CLENS_BIND_HOST"
CLENS_PROXY_ADDR="$(listen_addr "$CLENS_BIND_HOST" "$proxy_port")"
CLENS_ADMIN_ADDR="$(listen_addr "$CLENS_BIND_HOST" "$admin_port")"
if [ "$proxy_port" = "$admin_port" ]; then
  log error "--proxy-addr and --admin-addr must differ, both are '${proxy_port}'."
  exit 1
fi
case "$CLENS_BIND_HOST" in
  127.*|localhost|::1) ;;
  *) log warn "Binding to '${CLENS_BIND_HOST}' exposes the admin UI and the proxy, which have no authentication, to your network." ;;
esac

# ── Configure ANTHROPIC_BASE_URL via Claude Code's settings.json ───────
# claude-lens never sees this variable - Claude Code does, by reading the
# `env` block of its own ~/.claude/settings.json. We only touch that file,
# for the port this install's proxy listens on.
expected_anthropic_url="http://localhost:${proxy_port}"

claude_dir="${HOME}/.claude"
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

settings_action=""

# check_claude_settings validates ${settings_file} without changing it and
# sets settings_action to create, update or none, so a problem is reported
# before anything is installed.
check_claude_settings() {
  if [ ! -f "$settings_file" ]; then
    settings_action="create"
    return
  fi
  if ! command -v jq >/dev/null 2>&1; then
    log error "'${settings_file}' already exists and jq is required to safely read/update it, but jq is not installed."
    log error "Install jq (brew install jq) and re-run this installer, or add this yourself:"
    log error "$settings_json_snippet"
    exit 1
  fi
  if ! current_url="$(jq -r '.env.ANTHROPIC_BASE_URL // empty' "$settings_file" 2>/dev/null)"; then
    log error "'${settings_file}' exists but isn't valid JSON. Fix it manually, then re-run this installer. It should include:"
    log error "$settings_json_snippet"
    exit 1
  fi
  if [ -z "$current_url" ]; then
    settings_action="update"
  elif [ "$current_url" != "$expected_anthropic_url" ]; then
    log error "ANTHROPIC_BASE_URL is already set to '${current_url}' in ${settings_file}, but this install listens at '${expected_anthropic_url}' (from --proxy-addr=${proxy_port})."
    log error "Edit it manually to match, or re-run this installer with --proxy-addr matching the existing value."
    exit 1
  else
    settings_action="none"
  fi
}

# apply_claude_settings points Claude Code at this install's proxy by setting
# ANTHROPIC_BASE_URL in settings.json, leaving other keys untouched.
apply_claude_settings() {
  case "$settings_action" in
    create)
      log info "Creating ${settings_file}..."
      printf '%s\n' "$settings_json_snippet" > "$settings_file"
      log info "ANTHROPIC_BASE_URL set to ${expected_anthropic_url} in ${settings_file}."
      ;;
    update)
      orig_mode="$(stat -f '%Lp' "$settings_file")"
      tmp_settings="$(mktemp)"
      tmp_files+=("$tmp_settings")
      jq --arg url "$expected_anthropic_url" '.env.ANTHROPIC_BASE_URL = $url' "$settings_file" > "$tmp_settings"
      chmod "$orig_mode" "$tmp_settings"
      mv "$tmp_settings" "$settings_file"
      log info "ANTHROPIC_BASE_URL set to ${expected_anthropic_url} in ${settings_file}."
      ;;
    none)
      log info "ANTHROPIC_BASE_URL already set to ${expected_anthropic_url} in ${settings_file} - good."
      ;;
  esac
}

check_claude_settings
check_install_dir_writable
install_dir_existed=false
[ -d "$CLENS_INSTALL_DIR" ] && install_dir_existed=true
mkdir -p "$CLENS_DATA_DIR" "$CLENS_LOG_DIR"
chmod 700 "$CLENS_DATA_DIR" "$CLENS_LOG_DIR"
$install_dir_existed || chmod 700 "$CLENS_INSTALL_DIR"
log info "Binary: ${CLENS_INSTALL_DIR}/claude-lens, data: ${CLENS_DATA_DIR}, logs: ${CLENS_LOG_DIR}."

log info "Downloading latest claude-lens binary and checksum..."
tmp_bin="$(mktemp "${CLENS_INSTALL_DIR}/.claude-lens.XXXXXX")"
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

if [ "$CLENS_AS_SERVICE" = "true" ]; then
  mkdir -p "$HOME/Library/LaunchAgents"

  if launchctl list | grep -q "$LABEL" 2>/dev/null; then
    log info "Existing ${LABEL} service detected. Stopping service..."
    launchctl bootout "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || launchctl unload "$PLIST_PATH" 2>/dev/null || true
  fi
  rm -f "$PLIST_PATH"
fi

chmod 755 "$tmp_bin"
mv "$tmp_bin" "${CLENS_INSTALL_DIR}/claude-lens"

log info "Writing ${ENV_FILE}..."
tmp_env="$(mktemp "${CLENS_INSTALL_DIR}/.claude-lens.env.XXXXXX")"
tmp_files+=("$tmp_env")
cat <<EOF > "$tmp_env"
CLENS_PROXY_ADDR="${CLENS_PROXY_ADDR}"
CLENS_ADMIN_ADDR="${CLENS_ADMIN_ADDR}"
CLENS_DATA_DIR="${CLENS_DATA_DIR}"
CLENS_LOG_DIR="${CLENS_LOG_DIR}"
CLENS_AS_SERVICE="${CLENS_AS_SERVICE}"
EOF
chmod 600 "$tmp_env"
mv "$tmp_env" "$ENV_FILE"

if [ "$CLENS_AS_SERVICE" = "true" ]; then
  log info "Creating launchd property list..."
  cat <<EOF > "$PLIST_PATH"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${CLENS_INSTALL_DIR}/claude-lens</string>
    </array>
    <key>WorkingDirectory</key>
    <string>$(xml_escape "$CLENS_DATA_DIR")</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>CLENS_PROXY_ADDR</key>
        <string>$(xml_escape "$CLENS_PROXY_ADDR")</string>
        <key>CLENS_ADMIN_ADDR</key>
        <string>$(xml_escape "$CLENS_ADMIN_ADDR")</string>
        <key>CLENS_DATA_DIR</key>
        <string>$(xml_escape "$CLENS_DATA_DIR")</string>
        <key>CLENS_LOG_DIR</key>
        <string>$(xml_escape "$CLENS_LOG_DIR")</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$(xml_escape "$CLENS_LOG_DIR")/launchd-stdout.log</string>
    <key>StandardErrorPath</key>
    <string>$(xml_escape "$CLENS_LOG_DIR")/launchd-stderr.log</string>
</dict>
</plist>
EOF
  chmod 600 "$PLIST_PATH"

  log info "Loading launchd service..."
  launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || launchctl load -w "$PLIST_PATH"
  sleep 2
  if ! launchctl list "$LABEL" 2>/dev/null | grep -q '"PID"'; then
    log error "${LABEL} did not stay running. Check ${CLENS_LOG_DIR}/launchd-stderr.log"
    exit 1
  fi

  log info "${LABEL} installed/updated and started as a launchd agent!"
  log info "Proxy listening on ${CLENS_PROXY_ADDR}, admin UI on ${CLENS_ADMIN_ADDR}."
  log info "Config saved to ${ENV_FILE} - it's re-read on every install, but changing it by hand has no effect until you re-run this installer (launchd only reads env vars from the plist at load time)."
else
  log info "claude-lens binary installed/updated at ${CLENS_INSTALL_DIR}/claude-lens."
  log info "Config saved to ${ENV_FILE}."
  log info "Re-run this installer with --as-service to configure and start it as a launchd agent."
fi

apply_claude_settings
