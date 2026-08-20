#!/bin/bash
set -e

SERVICE_NAME="claude-lens"
INSTALL_DIR="/usr/local/bin"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
CONFIG_DIR="/etc/claude-lens"
ENV_FILE="${CONFIG_DIR}/claude-lens.env"
DOWNLOAD_URL="https://github.com/lfsc09/claude-lens/releases/latest/download/claude-lens-linux-amd64"
CHECKSUM_URL="${DOWNLOAD_URL}.sha256"
SERVICE_USER="nobody"
SERVICE_GROUP="nogroup"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

usage() {
  cat <<'USAGE'
Usage: install_linux.sh [options]

Safe to re-run at any time to update the binary and/or reconfigure the
service. Any option you omit keeps whatever was set on a previous run
(or the default shown, on a first install).

  --proxy-base-url URL      Upstream API URL (default: https://api.anthropic.com)
  --proxy-auth-token TOKEN  Authorization value forwarded upstream
  --proxy-custom-header "H: v"  Extra header forwarded upstream (repeatable)
  --proxy-addr ADDR         Proxy listen address (default: :7801)
  --admin-addr ADDR         Admin listen address (default: :7802)
  --data-dir PATH           SQLite database directory (default: /var/lib/claude-lens)
  --log-dir PATH            Log directory (default: /var/log/claude-lens)
  -h, --help                Show this help
USAGE
}

# ── Defaults ──────────────────────────────────────────────────────────
CLENS_PROXY_BASE_URL="https://api.anthropic.com"
CLENS_PROXY_AUTH_TOKEN=""
CLENS_PROXY_ADDR=":7801"
CLENS_ADMIN_ADDR=":7802"
CLENS_DATA_DIR="/var/lib/claude-lens"
CLENS_LOG_DIR="/var/log/claude-lens"
_CLENS_CUSTOM_HEADERS_ESCAPED=""

# ── Carry forward settings from a previous install, if any ─────────────
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  source "$ENV_FILE"
fi

# ── Parse flags (override persisted values/defaults) ────────────────────
PROXY_CUSTOM_HEADERS_ARR=()
while [ $# -gt 0 ]; do
  case "$1" in
    --proxy-base-url) CLENS_PROXY_BASE_URL="$2"; shift 2 ;;
    --proxy-base-url=*) CLENS_PROXY_BASE_URL="${1#*=}"; shift ;;
    --proxy-auth-token) CLENS_PROXY_AUTH_TOKEN="$2"; shift 2 ;;
    --proxy-auth-token=*) CLENS_PROXY_AUTH_TOKEN="${1#*=}"; shift ;;
    --proxy-custom-header) PROXY_CUSTOM_HEADERS_ARR+=("$2"); shift 2 ;;
    --proxy-custom-header=*) PROXY_CUSTOM_HEADERS_ARR+=("${1#*=}"); shift ;;
    --proxy-addr) CLENS_PROXY_ADDR="$2"; shift 2 ;;
    --proxy-addr=*) CLENS_PROXY_ADDR="${1#*=}"; shift ;;
    --admin-addr) CLENS_ADMIN_ADDR="$2"; shift 2 ;;
    --admin-addr=*) CLENS_ADMIN_ADDR="${1#*=}"; shift ;;
    --data-dir) CLENS_DATA_DIR="$2"; shift 2 ;;
    --data-dir=*) CLENS_DATA_DIR="${1#*=}"; shift ;;
    --log-dir) CLENS_LOG_DIR="$2"; shift 2 ;;
    --log-dir=*) CLENS_LOG_DIR="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

# Only replace the persisted custom headers if --proxy-custom-header was passed
# at least once this run. Stored pre-escaped (literal "\n" between
# entries) so it round-trips through `source` unambiguously and can be
# dropped straight into the unit file's Environment= line, whose own
# quoting rules (unlike EnvironmentFile's) reliably turn \n into a real
# newline regardless of systemd version.
if [ ${#PROXY_CUSTOM_HEADERS_ARR[@]} -gt 0 ]; then
  headers_escaped=""
  for h in "${PROXY_CUSTOM_HEADERS_ARR[@]}"; do
    if [ -z "$headers_escaped" ]; then
      headers_escaped="$h"
    else
      headers_escaped="${headers_escaped}\\n${h}"
    fi
  done
  _CLENS_CUSTOM_HEADERS_ESCAPED="$headers_escaped"
fi

if [ "$EUID" -ne 0 ]; then
  echo "Please run as root (e.g., sudo ./install_linux.sh)"
  exit 1
fi

# ── Verify ANTHROPIC_BASE_URL, the var Claude Code itself reads ────────
# claude-lens never sees this variable - Claude Code does, directly from
# the OS environment - so it has to be set independently of everything
# below. We only check it points at this install's proxy port.
proxy_port="${CLENS_PROXY_ADDR##*:}"
expected_anthropic_url="http://localhost:${proxy_port}"

if [ -n "${ANTHROPIC_BASE_URL:-}" ]; then
  if [ "$ANTHROPIC_BASE_URL" != "$expected_anthropic_url" ]; then
    echo "ERROR: ANTHROPIC_BASE_URL is set to '${ANTHROPIC_BASE_URL}', but this install listens at '${expected_anthropic_url}' (from --proxy-addr=${CLENS_PROXY_ADDR})." >&2
    echo "Fix this manually before continuing - either:" >&2
    echo "  export ANTHROPIC_BASE_URL=${expected_anthropic_url}" >&2
    echo "or re-run this installer with --proxy-addr matching your existing ANTHROPIC_BASE_URL port." >&2
    exit 1
  fi
  echo "ANTHROPIC_BASE_URL already points at ${expected_anthropic_url} - good."
else
  echo "NOTE: ANTHROPIC_BASE_URL is not set. Claude Code will not route through claude-lens until you set it and persist it in your shell profile:"
  echo "  export ANTHROPIC_BASE_URL=${expected_anthropic_url}"
fi

# ── Stop and disable if already running/installed ───────────────────────
if systemctl is-active --quiet "$SERVICE_NAME" || systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
  echo "Existing ${SERVICE_NAME} service detected. Stopping service..."
  systemctl stop "$SERVICE_NAME" || true
  systemctl disable "$SERVICE_NAME" || true
fi

if [ -f "$SERVICE_FILE" ]; then
  echo "Removing existing service file..."
  rm -f "$SERVICE_FILE"
  systemctl daemon-reload
fi

echo "Downloading latest ${SERVICE_NAME} binary and checksum..."
tmp_bin="$(mktemp)"
tmp_sha="$(mktemp)"
trap 'rm -f "$tmp_bin" "$tmp_sha"' EXIT
curl -fsSL "$DOWNLOAD_URL" -o "$tmp_bin"
curl -fsSL "$CHECKSUM_URL" -o "$tmp_sha"

expected_sha="$(awk '{print $1}' "$tmp_sha")"
actual_sha="$(sha256_of "$tmp_bin")"
if [ "$expected_sha" != "$actual_sha" ]; then
  echo "ERROR: checksum mismatch for downloaded binary (expected ${expected_sha}, got ${actual_sha})." >&2
  echo "Aborting - the existing installation, if any, was left untouched." >&2
  exit 1
fi

mv "$tmp_bin" "${INSTALL_DIR}/${SERVICE_NAME}"
chmod +x "${INSTALL_DIR}/${SERVICE_NAME}"

echo "Preparing data/log directories..."
mkdir -p "$CLENS_DATA_DIR" "$CLENS_LOG_DIR" "$CONFIG_DIR"
chown -R "${SERVICE_USER}:${SERVICE_GROUP}" "$CLENS_DATA_DIR" "$CLENS_LOG_DIR"

echo "Writing ${ENV_FILE}..."
cat <<EOF > "$ENV_FILE"
CLENS_PROXY_BASE_URL="${CLENS_PROXY_BASE_URL}"
CLENS_PROXY_AUTH_TOKEN="${CLENS_PROXY_AUTH_TOKEN}"
CLENS_PROXY_ADDR="${CLENS_PROXY_ADDR}"
CLENS_ADMIN_ADDR="${CLENS_ADMIN_ADDR}"
CLENS_DATA_DIR="${CLENS_DATA_DIR}"
CLENS_LOG_DIR="${CLENS_LOG_DIR}"
_CLENS_CUSTOM_HEADERS_ESCAPED="${_CLENS_CUSTOM_HEADERS_ESCAPED}"
EOF
chmod 600 "$ENV_FILE"
chown root:root "$ENV_FILE"

headers_env_line=""
if [ -n "$_CLENS_CUSTOM_HEADERS_ESCAPED" ]; then
  headers_env_line="Environment=\"CLENS_PROXY_CUSTOM_HEADERS=${_CLENS_CUSTOM_HEADERS_ESCAPED}\""
fi

echo "Creating systemd service unit..."
cat <<EOF > "$SERVICE_FILE"
[Unit]
Description=Claude Lens Background Service
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${SERVICE_NAME}
EnvironmentFile=-${ENV_FILE}
${headers_env_line}
WorkingDirectory=${CLENS_DATA_DIR}
Restart=always
RestartSec=5
User=${SERVICE_USER}
Group=${SERVICE_GROUP}

[Install]
WantedBy=multi-user.target
EOF

echo "Reloading systemd, enabling, and starting service..."
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl start "$SERVICE_NAME"

echo "${SERVICE_NAME} installed/updated and started successfully!"
echo "Proxy listening on ${CLENS_PROXY_ADDR}, admin UI on ${CLENS_ADMIN_ADDR}."
echo "Config saved to ${ENV_FILE} - editing the simple values there and running 'systemctl restart ${SERVICE_NAME}' applies them (custom headers need a re-run of this installer)."
