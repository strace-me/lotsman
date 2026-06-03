# lotsman-lib.sh — shared helpers for lotsman management CLIs.
# Sourced, not executed.  Provides config paths, safe mutation primitives
# (flock + timestamped backup + validate + restart + auto-rollback) and
# link/sub/QR helpers.  Mirrors the proven logic from panel.v2.
# shellcheck shell=bash

SB_CONF=${SB_CONF:-/etc/sing-box/config.json}
XR_CONF=${XR_CONF:-/usr/local/etc/xray/config.json}
XR_KEYS=${XR_KEYS:-/usr/local/etc/xray/.keys}
SUB_DIR=${SUB_DIR:-/opt/lotsman-sub}
BAK_DIR=${BAK_DIR:-/var/backups/lotsman-panel}
LOCK=${LOCK:-/run/lock/lotsman-panel.lock}
SUB_PORT=${SUB_PORT:-8080}
SNI=${SNI:-www.bing.com}
SB_CERT=${SB_CERT:-/etc/sing-box/cert.pem}
SB_KEY=${SB_KEY:-/etc/sing-box/key.pem}

# WAN iface autodetect (fallback ens3)
WAN=$(ip route get 8.8.8.8 2>/dev/null | grep -oP 'dev \K\S+' | head -1)
[[ -z $WAN || ! -e /sys/class/net/$WAN ]] && WAN=ens3

have(){ command -v "$1" >/dev/null 2>&1; }

PUBIP=""
pubip(){
  [[ -n $PUBIP ]] && { echo "$PUBIP"; return; }
  PUBIP=$(ip -4 addr show "$WAN" 2>/dev/null | grep -oP 'inet \K[0-9.]+' | head -1)
  [[ -z $PUBIP ]] && PUBIP=$(hostname -I 2>/dev/null | awk '{print $1}')
  echo "$PUBIP"
}

need_root(){ [[ $EUID -eq 0 ]] || { echo "This command must run as root (use sudo)." >&2; exit 1; }; }

# ---- user enumeration ----
sb_users(){ jq -r '[.inbounds[].users[]?.name]|unique|.[]' "$SB_CONF" 2>/dev/null; }
sb_ucount(){ jq -r '[.inbounds[].users[]?.name]|unique|length' "$SB_CONF" 2>/dev/null || echo 0; }
xr_users(){ jq -r '.inbounds[0].settings.clients[]?.email' "$XR_CONF" 2>/dev/null; }
sb_has_user(){ sb_users | grep -qx "$1"; }

# ---- backup / validate / restart ----
backup(){ local f=$1; mkdir -p "$BAK_DIR"; [[ -f $f ]] && cp -a "$f" "$BAK_DIR/$(basename "$f").$(date +%Y%m%d-%H%M%S).bak"; }
latest_bak(){ ls -t "$BAK_DIR/$(basename "$1")".*.bak 2>/dev/null | head -1; }

sb_validate_restart(){
  local out; out=$(sing-box check -c "$SB_CONF" 2>&1) || { echo "$out"; return 1; }
  systemctl restart sing-box; sleep 1
  systemctl is-active --quiet sing-box || { journalctl -u sing-box -n 15 --no-pager 2>&1; return 1; }
}
sb_rollback(){ local b; b=$(latest_bak "$SB_CONF"); [[ -n $b ]] && cp -a "$b" "$SB_CONF"; systemctl restart sing-box 2>/dev/null; }

# ---- sing-box link / sub helpers ----
OBFS_PW(){ jq -r '.inbounds[]|select(.type=="hysteria2").obfs.password' "$SB_CONF" 2>/dev/null; }

sb_links(){ local u=$1
  jq -r --arg u "$u" --arg ip "$(pubip)" --arg sni "$SNI" --arg obfs "$(OBFS_PW)" '
    (.inbounds[]|select(.type=="hysteria2").users[]?|select(.name==$u).password) as $pw |
    (.inbounds[]|select(.type=="tuic").users[]?|select(.name==$u)) as $t |
    if $pw and $t then
      "hysteria2://\($pw)@\($ip):2096/?obfs=salamander&obfs-password=\($obfs)&sni=\($sni)&insecure=1#lotsman-hy2-\($u)",
      "anytls://\($pw)@\($ip):8443/?sni=\($sni)&insecure=1#lotsman-anytls-\($u)",
      "tuic://\($t.uuid):\($t.password)@\($ip):2097/?congestion_control=bbr&alpn=h3&sni=\($sni)&allow_insecure=1&udp_relay_mode=native#lotsman-tuic-\($u)"
    else empty end' "$SB_CONF"
}

# find the existing sub token file for a user (by decoding its fragment)
sb_token_for(){ local u=$1 f frag
  for f in "$SUB_DIR"/*; do [[ -f $f ]] || continue
    frag=$(base64 -d <"$f" 2>/dev/null | grep -om1 "#lotsman-[a-z0-9]*-${u}\$")
    [[ -n $frag ]] && { basename "$f"; return; }
  done
}
# write/refresh the user's sub file; echoes token
sb_write_sub(){ local u=$1 tok; tok=$(sb_token_for "$u")
  [[ -z $tok ]] && tok=$(tr -dc 'A-Za-z0-9_-' </dev/urandom | head -c 32)
  sb_links "$u" | base64 -w0 > "$SUB_DIR/$tok"; chmod 644 "$SUB_DIR/$tok"; echo "$tok"
}
sub_url(){ echo "http://$(pubip):$SUB_PORT/$1"; }

# ---- xray (REALITY) link ----
xr_link(){ local email=$1 proto port uuid pbk sid sni ip
  proto=$(jq -r '.inbounds[0].protocol' "$XR_CONF" 2>/dev/null)
  port=$(jq -r '.inbounds[0].port' "$XR_CONF" 2>/dev/null)
  uuid=$(jq -r --arg e "$email" '.inbounds[0].settings.clients[]|select(.email==$e).id' "$XR_CONF" 2>/dev/null)
  pbk=$(awk -F': ' '/Password/{print $2}' "$XR_KEYS" 2>/dev/null)
  sid=$(awk -F': ' '/shortsid/{print $2}' "$XR_KEYS" 2>/dev/null)
  sni=$(jq -r '.inbounds[0].streamSettings.realitySettings.serverNames[0]' "$XR_CONF" 2>/dev/null)
  ip=$(pubip)
  echo "$proto://$uuid@$ip:$port?security=reality&sni=$sni&fp=firefox&pbk=$pbk&sid=$sid&spx=/&type=tcp&flow=xtls-rprx-vision&encryption=none#$email"
}

qr(){ have qrencode && printf '%s\n' "$1" | qrencode -t ansiutf8 2>/dev/null || echo "(qrencode missing)"; }
