#!/usr/bin/env bash
# Install or upgrade the Orchestrator relay on Ubuntu 24.04 (systemd 255).
# Idempotent; run as root. See README.md next to this file.
#
#   sudo ./install.sh --binary ./relay_linux_amd64 --domain relay.example.com [--email you@example.com] [--staging] [--no-ufw]
#   sudo ./install.sh --version relay-v0.1.0 --upgrade          # download from the GitHub release (public repo)
#   sudo relay-install --rollback                               # swap the previous binary back
set -euo pipefail

BIN=/usr/local/bin/relay
ENV_FILE=/etc/relay/relay.env
UNIT=/etc/systemd/system/relay.service
SELF=/usr/local/sbin/relay-install
REPO=markusbug/Orchestrator
STAGING_DIR=https://acme-staging-v02.api.letsencrypt.org/directory

binary="" version="" domain="${RELAY_DOMAIN:-}" email="${RELAY_ACME_EMAIL:-}"
staging=0 no_ufw=0 upgrade=0 rollback=0
deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() { sed -n '2,8p' "$0"; exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) binary="$2"; shift 2 ;;
    --version) version="$2"; shift 2 ;;
    --domain) domain="$2"; shift 2 ;;
    --email) email="$2"; shift 2 ;;
    --deploy-dir) deploy_dir="$2"; shift 2 ;;
    --staging) staging=1; shift ;;
    --no-ufw) no_ufw=1; shift ;;
    --upgrade) upgrade=1; shift ;;
    --rollback) rollback=1; shift ;;
    -h|--help) usage 0 ;;
    *) echo "unknown option: $1" >&2; usage 2 ;;
  esac
done

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd required" >&2; exit 1; }

log() { printf '\033[1m==> %s\033[0m\n' "$*"; }

health_local() {
  # Metrics listener serves /healthz without TLS; works before DNS and ACME.
  local addr
  addr="$(grep -E '^RELAY_METRICS_LISTEN=' "$ENV_FILE" 2>/dev/null | cut -d= -f2-)"
  addr="${addr:-127.0.0.1:9100}"
  curl -fsS --max-time 3 "http://$addr/healthz" >/dev/null 2>&1
}

wait_healthy() {
  local i
  for i in $(seq 1 20); do
    if systemctl is-active --quiet relay && health_local; then return 0; fi
    sleep 0.5
  done
  return 1
}

do_rollback() {
  [[ -x "$BIN.prev" ]] || { echo "no previous binary at $BIN.prev" >&2; exit 1; }
  log "rolling back to $BIN.prev ($("$BIN.prev" --version 2>/dev/null || echo unknown))"
  mv -f "$BIN" "$BIN.failed"
  mv -f "$BIN.prev" "$BIN"
  systemctl restart relay
  if wait_healthy; then
    log "rollback ok: $(curl -fsS http://127.0.0.1:9100/healthz 2>/dev/null || true)"
    rm -f "$BIN.failed"
  else
    echo "rollback did not become healthy; see: journalctl -u relay -n 50" >&2
    exit 1
  fi
}

if [[ $rollback -eq 1 ]]; then do_rollback; exit 0; fi

arch="$(uname -m)"
case "$arch" in
  x86_64) goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *) echo "unsupported arch $arch" >&2; exit 1 ;;
esac

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# --- resolve the binary and the deploy files ---------------------------------
if [[ -n "$version" ]]; then
  command -v curl >/dev/null || { echo "curl required" >&2; exit 1; }
  base="https://github.com/$REPO/releases/download/$version"
  log "downloading $version for linux/$goarch"
  curl -fsSL -o "$tmp/relay_linux_$goarch" "$base/relay_linux_$goarch"
  curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
  curl -fsSL -o "$tmp/relay-deploy.tar.gz" "$base/relay-deploy.tar.gz"
  (cd "$tmp" && sha256sum -c --ignore-missing SHA256SUMS)
  binary="$tmp/relay_linux_$goarch"
  tar -xzf "$tmp/relay-deploy.tar.gz" -C "$tmp"
  deploy_dir="$tmp/relay"
fi
if [[ -z "$binary" && ! -x "$BIN" ]]; then
  echo "need --binary PATH or --version relay-vX.Y.Z (no relay installed yet)" >&2; exit 1
fi
for f in relay.service 90-relay.conf journald.conf relay.env.example; do
  [[ -f "$deploy_dir/$f" ]] || { echo "missing $deploy_dir/$f (pass --deploy-dir)" >&2; exit 1; }
done

# --- binary (atomic swap, keep one previous) ---------------------------------
if [[ -n "$binary" ]]; then
  [[ -f "$binary" ]] || { echo "no such file: $binary" >&2; exit 1; }
  chmod +x "$binary"
  newver="$("$binary" --version 2>/dev/null || true)"
  [[ -n "$newver" ]] || { echo "$binary does not run here (wrong arch?)" >&2; exit 1; }
  if [[ -x "$BIN" ]] && cmp -s "$binary" "$BIN"; then
    log "binary unchanged ($newver)"
  else
    log "installing $newver to $BIN"
    install -m 0755 "$binary" "$BIN.new"
    [[ -x "$BIN" ]] && mv -f "$BIN" "$BIN.prev"
    mv -f "$BIN.new" "$BIN"
  fi
fi

# --- config ------------------------------------------------------------------
install -d -m 0750 /etc/relay
if [[ ! -f "$ENV_FILE" ]]; then
  if [[ $upgrade -eq 1 ]]; then echo "$ENV_FILE missing; run without --upgrade first" >&2; exit 1; fi
  if [[ -z "$domain" && -t 0 ]]; then read -rp "Relay domain (e.g. relay.example.com): " domain; fi
  [[ -n "$domain" ]] || { echo "--domain is required on first install" >&2; exit 1; }
  log "writing $ENV_FILE"
  sed -e "s|^RELAY_DOMAIN=.*|RELAY_DOMAIN=$domain|" \
      -e "s|^RELAY_ACME_EMAIL=.*|RELAY_ACME_EMAIL=$email|" \
      "$deploy_dir/relay.env.example" > "$ENV_FILE"
  if [[ $staging -eq 1 ]]; then
    sed -i "s|^#RELAY_ACME_DIRECTORY=.*|RELAY_ACME_DIRECTORY=$STAGING_DIR|" "$ENV_FILE"
  fi
  chmod 0600 "$ENV_FILE"
else
  [[ -n "$domain" ]] && log "note: $ENV_FILE exists; --domain ignored (edit the file to change it)"
fi
domain="$(grep -E '^RELAY_DOMAIN=' "$ENV_FILE" | cut -d= -f2- || true)"
if [[ -z "$domain" ]]; then
  echo "RELAY_DOMAIN is not set in $ENV_FILE; add it and rerun (the relay will not start without it)" >&2
  exit 1
fi

# --- unit, sysctl, journald --------------------------------------------------
install -m 0644 "$deploy_dir/relay.service" "$UNIT"
install -m 0644 "$deploy_dir/90-relay.conf" /etc/sysctl.d/90-relay.conf
install -d /etc/systemd/journald.conf.d
if ! cmp -s "$deploy_dir/journald.conf" /etc/systemd/journald.conf.d/relay.conf 2>/dev/null; then
  install -m 0644 "$deploy_dir/journald.conf" /etc/systemd/journald.conf.d/relay.conf
  systemctl restart systemd-journald || true
fi
sysctl --system >/dev/null

# --- firewall ----------------------------------------------------------------
if [[ $no_ufw -eq 0 ]] && command -v ufw >/dev/null; then
  log "ufw: allow 22/tcp and 443/tcp"
  ufw allow 22/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  if ufw status | grep -q inactive; then ufw --force enable >/dev/null; fi
  if [[ -f "$deploy_dir/91-relay-conntrack.conf" ]]; then
    install -m 0644 "$deploy_dir/91-relay-conntrack.conf" /etc/sysctl.d/91-relay-conntrack.conf
    echo nf_conntrack > /etc/modules-load.d/relay.conf
    echo "options nf_conntrack hashsize=262144" > /etc/modprobe.d/relay.conf
    modprobe nf_conntrack || true
    sysctl --system >/dev/null
  fi
elif [[ $no_ufw -eq 1 ]]; then
  log "firewall: skipped (use the provider firewall: allow TCP 22, TCP 443, ICMP)"
fi

# --- start -------------------------------------------------------------------
systemctl daemon-reload
systemctl enable relay >/dev/null 2>&1 || true
log "restarting relay"
systemctl restart relay
if ! wait_healthy; then
  echo "relay is not healthy; last log lines:" >&2
  journalctl -u relay -n 20 --no-pager >&2 || true
  if [[ $upgrade -eq 1 && -x "$BIN.prev" ]]; then do_rollback; fi
  exit 1
fi

install -m 0755 "$0" "$SELF" 2>/dev/null || true
systemctl --no-pager status relay | head -n 12 || true
echo
log "relay is running: $(curl -fsS http://127.0.0.1:9100/healthz 2>/dev/null || true)"
if curl -fsS --max-time 20 --resolve "$domain:443:127.0.0.1" "https://$domain/healthz" >/dev/null 2>&1; then
  log "public check ok: https://$domain/healthz"
else
  cat <<MSG
Public check https://$domain/healthz did not succeed yet. That is expected on
first start until DNS points here and the certificate is issued. Check with:
  journalctl -u relay -f
  curl -sS https://$domain/healthz
MSG
fi
