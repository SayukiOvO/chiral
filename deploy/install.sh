#!/bin/sh
# Chiral installer — panel or node, from the published release.
#
#   curl -fsSL https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/install.sh | sh
#
# Non-interactive (what the console's "add node" command uses):
#   ... | sh -s -- --agent --panel-url HOST:PORT --token TOKEN
#
# POSIX sh on purpose: this runs on whatever a fresh VPS came with.
set -eu

REPO=SayukiOvO/chiral
BIN=/usr/local/bin
ETC=/etc/chiral
XRAY_ASSETS=/usr/local/share/xray

RED=''; GRN=''; YEL=''; DIM=''; OFF=''
if [ -t 1 ]; then RED=$(printf '\033[31m'); GRN=$(printf '\033[32m')
  YEL=$(printf '\033[33m'); DIM=$(printf '\033[2m'); OFF=$(printf '\033[0m'); fi
say()  { printf '%s\n' "$*"; }
step() { printf '%s==>%s %s\n' "$GRN" "$OFF" "$*"; }
warn() { printf '%s !%s %s\n' "$YEL" "$OFF" "$*"; }
die()  { printf '%s !!%s %s\n' "$RED" "$OFF" "$*" >&2; exit 1; }

ROLE=""; PANEL_URL=""; JOIN_TOKEN=""; DOMAIN=""; ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --panel)     ROLE=panel ;;
    --agent)     ROLE=agent ;;
    --panel-url) PANEL_URL="$2"; shift ;;
    --token)     JOIN_TOKEN="$2"; shift ;;
    --domain)    DOMAIN="$2"; shift ;;
    -y|--yes)    ASSUME_YES=1 ;;
    -h|--help)
      sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

[ "$(id -u)" = 0 ] || die "run this as root (sudo sh -c '...' or pipe into sudo sh)"
command -v systemctl >/dev/null 2>&1 || die "this installer needs systemd"

case "$(uname -s)" in Linux) ;; *) die "Linux only; on macOS use the container images" ;; esac
case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64; XRAY_ARCH=64 ;;
  aarch64|arm64) GOARCH=arm64; XRAY_ARCH=arm64-v8a ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

need() { command -v "$1" >/dev/null 2>&1 || MISSING="$MISSING $1"; }
MISSING=""; need curl; need tar; need unzip
[ -n "$MISSING" ] && die "missing:$MISSING — install them and re-run"

ask() { # ask VAR "prompt" "default"
  eval "_cur=\${$1:-}"
  [ -n "$_cur" ] && return 0
  if [ "$ASSUME_YES" = 1 ] || [ ! -t 0 ]; then
    [ -n "$3" ] || die "$2 is required (pass it as a flag when running non-interactively)"
    eval "$1=\$3"; return 0
  fi
  if [ -n "$3" ]; then printf '%s [%s]: ' "$2" "$3" >&2
  else printf '%s: ' "$2" >&2; fi
  read -r _v </dev/tty || _v=""
  [ -z "$_v" ] && _v="$3"
  eval "$1=\$_v"
}

# ---------------------------------------------------------------- release

step "Finding the latest release"
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$TAG" ] || die "could not reach the GitHub API to find a release"
say "    $TAG"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
PKG="chiral-${TAG}-linux-${GOARCH}"
step "Downloading $PKG"
curl -fsSL -o "$TMP/pkg.tar.gz" \
  "https://github.com/$REPO/releases/download/$TAG/${PKG}.tar.gz" \
  || die "download failed"
tar -xzf "$TMP/pkg.tar.gz" -C "$TMP"
SRC="$TMP/$PKG"

# ---------------------------------------------------------------- xray

install_xray() {
  [ -x "$BIN/xray" ] && { say "    xray already at $BIN/xray"; return 0; }
  step "Installing Xray-core"
  XV=$(curl -fsSL "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=1" \
       | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$XV" ] || die "could not determine an Xray-core version"
  curl -fsSL -o "$TMP/xray.zip" \
    "https://github.com/XTLS/Xray-core/releases/download/$XV/Xray-linux-${XRAY_ARCH}.zip" \
    || die "could not download Xray-core $XV"
  mkdir -p "$TMP/xray" && unzip -oq "$TMP/xray.zip" -d "$TMP/xray"
  install -m755 "$TMP/xray/xray" "$BIN/xray"
  install -d -m755 "$XRAY_ASSETS"
  # Routing rules using geosite:/geoip: fail to load without these, and the
  # panel would then reject configs that are perfectly valid on the node.
  install -m644 "$TMP/xray/geoip.dat" "$TMP/xray/geosite.dat" "$XRAY_ASSETS/"
  say "    $XV"
}

# ---------------------------------------------------------------- role

if [ -z "$ROLE" ]; then
  say ""
  say "  1) Panel  — the control plane. One per deployment."
  say "  2) Node   — runs Xray and serves your users."
  say ""
  ask ROLE_N "Install which" "1"
  case "$ROLE_N" in 1|panel) ROLE=panel ;; 2|agent|node) ROLE=agent ;; *) die "pick 1 or 2" ;; esac
fi

install -d -m755 "$ETC"

if [ "$ROLE" = panel ]; then
  install_xray
  step "Installing the panel"
  install -m755 "$SRC/chiral-core" "$BIN/chiral-core"
  install -m644 "$SRC/chiral-core.service" /etc/systemd/system/

  if [ -f "$ETC/core.env" ]; then
    warn "$ETC/core.env exists; leaving it alone"
  else
    say ""
    ask DOMAIN "Panel domain" ""
    [ -n "$DOMAIN" ] || die "a domain is required"

    say ""
    say "  1) Behind a reverse proxy  ${DIM}(nginx, Caddy — it holds the certificate)${OFF}"
    say "  2) Serve HTTPS directly    ${DIM}(you provide a certificate and key)${OFF}"
    say ""
    ask TLS_MODE "TLS" "1"

    TLS_LINES=""
    LISTEN_HTTP="127.0.0.1:8080"
    LISTEN_GRPC="127.0.0.1:8443"
    TRUSTED="CHIRAL_TRUSTED_PROXY=127.0.0.1"
    if [ "$TLS_MODE" = 2 ]; then
      ask TLS_CERT "  Certificate (fullchain.pem)" "/etc/chiral/tls/fullchain.pem"
      ask TLS_KEY  "  Private key" "/etc/chiral/tls/privkey.pem"
      [ -r "$TLS_CERT" ] || warn "$TLS_CERT is not readable yet — put it there before starting"
      TLS_LINES="CHIRAL_TLS_CERT=$TLS_CERT
CHIRAL_TLS_KEY=$TLS_KEY"
      LISTEN_HTTP=":443"
      LISTEN_GRPC=":8443"
      TRUSTED="# CHIRAL_TRUSTED_PROXY="
      # A dynamic UID cannot read a root-owned key, so hand it over via systemd.
      mkdir -p /etc/systemd/system/chiral-core.service.d
      cat > /etc/systemd/system/chiral-core.service.d/tls.conf <<UNIT
[Service]
# Serving TLS directly: bind 443, and read the key as root before dropping to
# the dynamic UID.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
LoadCredential=tls-cert:$TLS_CERT
LoadCredential=tls-key:$TLS_KEY
UNIT
      TLS_LINES="CHIRAL_TLS_CERT=%d/tls-cert
CHIRAL_TLS_KEY=%d/tls-key"
    fi

    ADMIN_TOKEN=$(head -c32 /dev/urandom | base64 | tr -d '\n=' | tr '+/' '-_')
    SECRET_KEY=$(head -c32 /dev/urandom | base64 | tr -d '\n')

    cat > "$ETC/core.env" <<EOF
# Written by the installer. Full reference: docs/deployment.md

CHIRAL_PUBLIC_URL=https://$DOMAIN
CHIRAL_ADMIN_TOKEN=$ADMIN_TOKEN

# Decrypts every private key, subscription token and recorded address in the
# database. Back it up; losing it loses all of them.
CHIRAL_SECRET_KEY=$SECRET_KEY

CHIRAL_DB_PATH=/var/lib/chiral/chiral.db
CHIRAL_KERNEL_DIR=/var/lib/chiral/kernels
CHIRAL_XRAY_BIN=$BIN/xray
XRAY_LOCATION_ASSET=$XRAY_ASSETS

CHIRAL_HTTP_LISTEN=$LISTEN_HTTP
CHIRAL_GRPC_LISTEN=$LISTEN_GRPC
$TRUSTED
$TLS_LINES

# Subscriber portal: off | closed | open
CHIRAL_PORTAL_MODE=off
EOF
    chmod 600 "$ETC/core.env"
  fi

  systemctl daemon-reload
  systemctl enable --now chiral-core >/dev/null 2>&1 || die "chiral-core failed to start — journalctl -u chiral-core"
  sleep 2
  systemctl is-active --quiet chiral-core || die "chiral-core is not running — journalctl -u chiral-core -n 30"

  PW=$(journalctl -u chiral-core --no-pager 2>/dev/null \
       | sed -n 's/.*this password is shown once.*password=\([^ ]*\).*/\1/p' | tail -1)

  say ""
  step "Panel installed"
  say ""
  say "  Sign in at ${GRN}https://${DOMAIN}/admin/${OFF}"
  say "  Username: admin"
  [ -n "$PW" ] && say "  Password: ${GRN}${PW}${OFF}   ${DIM}(shown once; also in journalctl)${OFF}"
  say ""
  if [ "${TLS_MODE:-1}" = 2 ]; then
    say "  Serving HTTPS directly on 443. Agents dial ${DOMAIN}:8443."
    say "${DIM}  Renew the certificate in place, then: systemctl restart chiral-core${OFF}"
  else
    warn "Nothing listens on 443 yet — put a reverse proxy in front:"
    say ""
    say "    ${DOMAIN} {"
    say "        reverse_proxy 127.0.0.1:8080"
    say "    }"
    say "    ${DOMAIN}:8443 {"
    say "        reverse_proxy h2c://127.0.0.1:8443"
    say "    }"
    say ""
    say "${DIM}  Caddyfile syntax. nginx and the reasoning: docs/deployment.md${OFF}"
  fi

else
  [ -n "$PANEL_URL" ] || ask PANEL_URL "Panel address (host:port)" ""
  [ -n "$PANEL_URL" ] || die "the panel address is required"
  [ -n "$JOIN_TOKEN" ] || ask JOIN_TOKEN "Join token from the console" ""
  [ -n "$JOIN_TOKEN" ] || die "a join token is required"

  install_xray
  step "Installing the agent"
  install -m755 "$SRC/chiral-agent" "$BIN/chiral-agent"
  install -m644 "$SRC/chiral-agent.service" /etc/systemd/system/

  cat > "$ETC/agent.env" <<EOF
# Written by the installer.
PANEL_URL=$PANEL_URL
JOIN_TOKEN=$JOIN_TOKEN
CHIRAL_STATE_DIR=/var/lib/chiral-agent
CHIRAL_XRAY_BIN=$BIN/xray
XRAY_LOCATION_ASSET=$XRAY_ASSETS
EOF
  chmod 600 "$ETC/agent.env"

  systemctl daemon-reload
  systemctl enable --now chiral-agent >/dev/null 2>&1 || die "chiral-agent failed to start — journalctl -u chiral-agent"
  sleep 3
  systemctl is-active --quiet chiral-agent || die "chiral-agent is not running — journalctl -u chiral-agent -n 30"

  say ""
  step "Node installed"
  say ""
  if journalctl -u chiral-agent --no-pager -n 50 2>/dev/null | grep -q "stream established"; then
    say "  ${GRN}Connected to the panel.${OFF} It should appear in the console now."
  else
    warn "Started, but no connection to the panel yet."
    say "${DIM}    journalctl -u chiral-agent -f${OFF}"
  fi
fi
