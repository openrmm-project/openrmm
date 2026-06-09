#!/usr/bin/env bash
# force-xorg: detect a Wayland display server and switch the system to Xorg so
# RMM native remote works (X11 capture + XTEST input + login-screen seat-following).
# Idempotent and auditable: prints every action to STDOUT, only changes what's
# needed, and keeps a backup of any edited config. Requires root.
#
# By default the change applies on the NEXT reboot (restarting the display
# manager would log the active user out). Set RMM_FORCE_REBOOT=1 to reboot now.
set -u
P='[force-xorg]'
log(){ echo "$P $*"; }
changed=0

[ "$(id -u)" = 0 ] || { log "ERROR: must run as root"; exit 1; }

# --- current session type (best effort) ---
seat_session="$(loginctl 2>/dev/null | awk '/seat0/{print $1; exit}')"
sess_type=""
[ -n "${seat_session:-}" ] && sess_type="$(loginctl show-session "$seat_session" -p Type --value 2>/dev/null)"
log "active seat0 session: ${seat_session:-none} type=${sess_type:-unknown}"

# --- active display manager ---
dm=""
[ -e /etc/systemd/system/display-manager.service ] && \
  dm="$(basename "$(readlink -f /etc/systemd/system/display-manager.service)" .service)"
log "display manager: ${dm:-unknown}"

# --- GDM: WaylandEnable=false covers the greeter AND offered user sessions ---
gdm_conf=""
for c in /etc/gdm3/custom.conf /etc/gdm/custom.conf; do
  [ -f "$c" ] && gdm_conf="$c" && break
done
if [ -n "$gdm_conf" ]; then
  log "GDM config: $gdm_conf"
  if grep -qiE '^[[:space:]]*WaylandEnable[[:space:]]*=[[:space:]]*false' "$gdm_conf"; then
    log "WaylandEnable already false - no change"
  else
    cp -a "$gdm_conf" "${gdm_conf}.rmmbak.$(date +%Y%m%d%H%M%S)"
    if grep -qiE '^[[:space:]]*#?[[:space:]]*WaylandEnable[[:space:]]*=' "$gdm_conf"; then
      sed -i -E 's/^[[:space:]]*#?[[:space:]]*WaylandEnable[[:space:]]*=.*/WaylandEnable=false/I' "$gdm_conf"
    elif grep -qiE '^[[:space:]]*\[daemon\]' "$gdm_conf"; then
      sed -i -E '0,/^[[:space:]]*\[daemon\]/s//[daemon]\nWaylandEnable=false/' "$gdm_conf"
    else
      printf '\n[daemon]\nWaylandEnable=false\n' >> "$gdm_conf"
    fi
    log "set WaylandEnable=false (backup: ${gdm_conf}.rmmbak.*)"
    changed=1
  fi
fi

# --- SDDM (KDE): force x11 ---
if [ "$dm" = sddm ]; then
  mkdir -p /etc/sddm.conf.d
  scfg=/etc/sddm.conf.d/10-rmm-xorg.conf
  if [ -f "$scfg" ] && grep -q 'DisplayServer=x11' "$scfg"; then
    log "SDDM already x11"
  else
    printf '[General]\nDisplayServer=x11\n' > "$scfg"
    log "wrote $scfg (DisplayServer=x11)"
    changed=1
  fi
fi

# --- LightDM is X11 by default ---
[ "$dm" = lightdm ] && log "LightDM is X11 by default - no change"

# --- verdict ---
if [ "$changed" = 0 ]; then
  if [ "$sess_type" = x11 ]; then
    log "RESULT: already on Xorg and config enforces it - nothing to do."
  else
    log "RESULT: no config change applied. If still Wayland, ensure an Xorg session package is installed (e.g. gnome-session on Ubuntu)."
  fi
  exit 0
fi

log "RESULT: Xorg enforced. A REBOOT is required to take effect."
if [ "${RMM_FORCE_REBOOT:-0}" = 1 ]; then
  log "RMM_FORCE_REBOOT=1 -> rebooting in 5s"
  ( sleep 5; systemctl reboot ) >/dev/null 2>&1 &
else
  log "Reboot when convenient: 'systemctl reboot' (or 'systemctl restart ${dm:-display-manager}', which logs out the active user)."
fi
exit 0
