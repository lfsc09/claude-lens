#!/bin/bash
set -e

LABEL="com.user.claude-lens"
PLIST_PATH="$HOME/Library/LaunchAgents/${LABEL}.plist"
CONFIG_DIR="$HOME/Library/Application Support/claude-lens"
ENV_FILE="${CONFIG_DIR}/claude-lens.env"

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

usage() {
  cat <<'USAGE'
Usage: install_macos.sh [options]

Safe to re-run at any time to update the binary and/or reconfigure the
service. Any option you omit keeps whatever was set on a previous run
(or the default shown, on a first install).

  --proxy-addr PORT         Proxy listen port (default: 7801)
  --admin-addr PORT         Admin listen port (default: 7802)
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

# ── Defaults ──────────────────────────────────────────────────────────
CLENS_PROXY_ADDR="7801"
CLENS_ADMIN_ADDR="7802"
CLENS_INSTALL_DIR=""
CLENS_AS_SERVICE="false"

# ── Carry forward settings from a previous install, if any ─────────────
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  source "$ENV_FILE"
fi

# ── Parse flags (override persisted values/defaults) ────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --proxy-addr) CLENS_PROXY_ADDR="$2"; shift 2 ;;
    --proxy-addr=*) CLENS_PROXY_ADDR="${1#*=}"; shift ;;
    --admin-addr) CLENS_ADMIN_ADDR="$2"; shift 2 ;;
    --admin-addr=*) CLENS_ADMIN_ADDR="${1#*=}"; shift ;;
    --install-dir) CLENS_INSTALL_DIR="$2"; shift 2 ;;
    --install-dir=*) CLENS_INSTALL_DIR="${1#*=}"; shift ;;
    --as-service) CLENS_AS_SERVICE="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) log error "Unknown option: $1"; usage; exit 1 ;;
  esac
done

# --proxy-addr/--admin-addr accept a bare port number; normalized here to
# the ":port" form CLENS_PROXY_ADDR/CLENS_ADMIN_ADDR carry at runtime.
CLENS_PROXY_ADDR=":$(normalize_port --proxy-addr "$CLENS_PROXY_ADDR")"
CLENS_ADMIN_ADDR=":$(normalize_port --admin-addr "$CLENS_ADMIN_ADDR")"
if [ "$CLENS_PROXY_ADDR" = "$CLENS_ADMIN_ADDR" ]; then
  log error "--proxy-addr and --admin-addr must differ, both are '${CLENS_PROXY_ADDR#:}'."
  exit 1
fi

# ── Resolve install/data/log directories ─────────────────────────────────
CLENS_INSTALL_DIR="${CLENS_INSTALL_DIR:-${HOME}/.claude-lens}"
case "$CLENS_INSTALL_DIR" in
  "~"|"~/"*) CLENS_INSTALL_DIR="${HOME}${CLENS_INSTALL_DIR#\~}" ;;
esac
case "$CLENS_INSTALL_DIR" in
  /*) ;;
  *) log error "--install-dir must be an absolute path, got '${CLENS_INSTALL_DIR}'."; exit 1 ;;
esac
CLENS_DATA_DIR="${CLENS_INSTALL_DIR}/data"
CLENS_LOG_DIR="${CLENS_INSTALL_DIR}/logs"

# ── Configure ANTHROPIC_BASE_URL via Claude Code's settings.json ───────
# claude-lens never sees this variable - Claude Code does, by reading the
# `env` block of its own ~/.claude/settings.json. We only touch that file,
# for the port this install's proxy listens on.
proxy_port="${CLENS_PROXY_ADDR##*:}"
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

# configure_claude_settings points Claude Code at this install's proxy by
# setting ANTHROPIC_BASE_URL in settings.json, leaving other keys untouched.
configure_claude_settings() {
  if [ ! -f "$settings_file" ]; then
    log info "Creating ${settings_file}..."
    printf '%s\n' "$settings_json_snippet" > "$settings_file"
    log info "ANTHROPIC_BASE_URL set to ${expected_anthropic_url} in ${settings_file}."
  else
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
      orig_mode="$(stat -f '%Lp' "$settings_file")"
      tmp_settings="$(mktemp)"
      tmp_files+=("$tmp_settings")
      jq --arg url "$expected_anthropic_url" '.env.ANTHROPIC_BASE_URL = $url' "$settings_file" > "$tmp_settings"
      chmod "$orig_mode" "$tmp_settings"
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

check_install_dir_writable
mkdir -p "$CLENS_DATA_DIR" "$CLENS_LOG_DIR" "$CONFIG_DIR"

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
cat <<EOF > "$ENV_FILE"
CLENS_PROXY_ADDR="${CLENS_PROXY_ADDR}"
CLENS_ADMIN_ADDR="${CLENS_ADMIN_ADDR}"
CLENS_INSTALL_DIR="${CLENS_INSTALL_DIR}"
CLENS_DATA_DIR="${CLENS_DATA_DIR}"
CLENS_LOG_DIR="${CLENS_LOG_DIR}"
CLENS_AS_SERVICE="${CLENS_AS_SERVICE}"
EOF
chmod 600 "$ENV_FILE"

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

configure_claude_settings
