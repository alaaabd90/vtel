#!/usr/bin/env bash
# vtel VPS installer
# Usage: curl -sL https://raw.githubusercontent.com/alaaabd90/vtel/main/scripts/install.sh | bash
#
# Installs the vtel binary + CLI manager and, if a config with at least one
# link already exists, (re)installs and starts the systemd service. vtel
# cannot provision bot links itself - each one needs a real Telegram bot
# token from @BotFather - so a fresh install writes an empty config
# skeleton and tells you to add links (vtel links add) before the service
# has anything to do.
#
# Defaults to installing the SERVER role (the side with internet access,
# typically what you'd put on a VPS - matches gdrive's own install.sh
# setting up its exit-node role). Set VTEL_MODE=client before piping into
# bash to install the client role instead:
#   curl -sL .../install.sh | VTEL_MODE=client bash
set -euo pipefail

REPO="alaaabd90/vtel"
BINARY_NAME_AMD64="vtel-linux-amd64"
BINARY_NAME_ARM64="vtel-linux-arm64"
INSTALL_PATH="/usr/local/bin/vtel"
VTEL_ROOT="/root/vtel"
CONFIG_PATH="$VTEL_ROOT/config.json"
SERVICE_NAME="vtel"
MODE="${VTEL_MODE:-server}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
ok()   { echo -e "  ${GREEN}[OK]${NC}  $*"; }
warn() { echo -e "  ${YELLOW}[!!]${NC}  $*"; }
err()  { echo -e "  ${RED}[ERR]${NC} $*"; }
info() { echo -e "  ${CYAN}[*]${NC}  $*"; }

require_root() {
    [[ "$(id -u)" -eq 0 ]] || { err "Run as root."; exit 1; }
}

require_mode() {
    if [[ "$MODE" != "server" && "$MODE" != "client" ]]; then
        err "VTEL_MODE must be 'server' or 'client', got '$MODE'"
        exit 1
    fi
}

detect_binary_name() {
    case "$(uname -m)" in
        x86_64|amd64) echo "$BINARY_NAME_AMD64" ;;
        aarch64|arm64) echo "$BINARY_NAME_ARM64" ;;
        *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
    esac
}

download_binary() {
    echo ""
    local binary_name
    binary_name=$(detect_binary_name)
    info "Fetching latest release from github.com/$REPO..."
    local api_url="https://api.github.com/repos/$REPO/releases/latest"
    local release_json
    release_json=$(curl -fsSL --max-time 30 "$api_url") || { err "Failed to fetch release info"; exit 1; }
    local tag
    tag=$(echo "$release_json" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    local download_url
    download_url=$(echo "$release_json" | grep "browser_download_url" | grep "$binary_name\"" | head -1 | sed 's/.*"browser_download_url": *"\([^"]*\)".*/\1/')
    if [[ -z "$download_url" ]]; then
        err "Could not find $binary_name in release $tag"
        exit 1
    fi
    info "Downloading $tag/$binary_name..."
    curl -fsSL --max-time 120 -o "$INSTALL_PATH.new" "$download_url" || { err "Download failed"; exit 1; }
    chmod +x "$INSTALL_PATH.new"
    mv "$INSTALL_PATH.new" "$INSTALL_PATH"
    ok "Installed $INSTALL_PATH ($tag)"
}

random_secret() {
    # 32 random bytes, hex-encoded -> a 64-char secret with no shell-quoting
    # surprises once it's embedded in JSON.
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

create_config_skeleton() {
    echo ""
    mkdir -p "$VTEL_ROOT"
    if [[ -f "$CONFIG_PATH" ]]; then
        info "Existing config found at $CONFIG_PATH, leaving it as-is."
        return
    fi
    info "Creating a fresh config skeleton at $CONFIG_PATH (mode: $MODE)..."
    local secret
    secret=$(random_secret)
    local listen_line=""
    if [[ "$MODE" == "client" ]]; then
        listen_line='"listen": "127.0.0.1:1080",'
    fi
    cat > "$CONFIG_PATH" << EOF
{
  "mode": "$MODE",
  $listen_line
  "secret": "$secret",
  "compression_level": "fastest",
  "reject_ipv6": false,
  "quiet_hours": null,
  "links": []
}
EOF
    chmod 600 "$CONFIG_PATH"
    ok "Created $CONFIG_PATH with a random secret"
    warn "Copy this exact secret to the peer side (client<->server must share it): $secret"
    warn "No links yet. Add at least one before the service can run:"
    warn "  vtel links add"
    warn "Then start the service: vtel install"
}

install_service_if_ready() {
    echo ""
    if [[ ! -f "$CONFIG_PATH" ]]; then
        warn "No config at $CONFIG_PATH - skipping service install."
        return
    fi
    local link_count
    # grep -c already prints "0" on zero matches (and exits 1); the fallback
    # must sit outside the substitution or its own "0" doubles up with
    # grep's, producing "0\n0" and breaking the -eq test below.
    link_count=$(grep -c '"token"' "$CONFIG_PATH" 2>/dev/null) || link_count=0
    if [[ "$link_count" -eq 0 ]]; then
        warn "No links configured yet - skipping service install."
        warn "Run 'vtel links add' to add one, then 'vtel install' to start the service."
        return
    fi
    info "Installing and starting the $SERVICE_NAME service ($link_count link(s) found)..."
    if "$INSTALL_PATH" install; then
        ok "$SERVICE_NAME is running"
    else
        err "$SERVICE_NAME failed to start - check: journalctl -u $SERVICE_NAME -n 50"
    fi
}

print_status() {
    echo ""
    info "vtel CLI installed at $INSTALL_PATH - run 'vtel' for the interactive manager."
    ok "Install complete."
}

main() {
    echo -e "\n${CYAN}== vtel VPS Installer (mode: $MODE) ==${NC}\n"
    require_root
    require_mode
    download_binary
    create_config_skeleton
    install_service_if_ready
    print_status
}

main "$@"
