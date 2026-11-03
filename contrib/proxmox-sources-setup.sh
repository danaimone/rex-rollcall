#!/usr/bin/env bash
# Run as root on a Proxmox host:
#   sudo SRC_BIN=... WAKE_SRC=... CT=... USER_NAME=... bash contrib/proxmox-sources-setup.sh
set -euo pipefail

SRC_BIN=${SRC_BIN:?set SRC_BIN to your rex-rollcall binary}
WAKE_SRC=${WAKE_SRC:?set WAKE_SRC to your wake-cli binary}
BIN=/usr/local/bin/rex-rollcall
CT=${CT:?set CT to the container id}
USER_NAME=${USER_NAME:?set USER_NAME to the account that runs the picker}
SUDOERS=/etc/sudoers.d/rex-rollcall
TMPFILES=/etc/tmpfiles.d/rex-rollcall.conf

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
[ -x "$SRC_BIN" ] || { echo "missing $SRC_BIN" >&2; exit 1; }
[ -x "$WAKE_SRC" ] || { echo "missing $WAKE_SRC" >&2; exit 1; }
pct status "$CT" | grep -q running || { echo "CT $CT is not running" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "== binary for root on the host: $BIN"
install -o root -g root -m 0755 "$SRC_BIN" "$BIN"
"$BIN" version

echo "== binary in CT $CT: $BIN"
pct push "$CT" "$BIN" "$BIN" --user 0 --group 0 --perms 0755
pct exec "$CT" -- "$BIN" version

echo "== unlock stamp directory: /run/rex-rollcall"
printf 'd /run/rex-rollcall 0700 root root -\n' > "$work/tmpfiles"
install -o root -g root -m 0644 "$work/tmpfiles" "$TMPFILES"
systemd-tmpfiles --create "$TMPFILES"
stat -c '%U:%G %a %n' /run/rex-rollcall

cat > "$work/wake-refresh.service" <<'EOF'
[Unit]
Description=Refresh root's Wake agent-session index
ConditionPathExists=/root/.local/share/wake/wake.db

[Service]
Type=oneshot
ExecStart=/root/.local/opt/wake/wake-cli refresh
EOF
cat > "$work/wake-refresh.timer" <<'EOF'
[Unit]
Description=Refresh root's Wake agent-session index every 10 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=10min

[Install]
WantedBy=timers.target
EOF

echo "== Wake for root on the host"
install -D -o root -g root -m 0755 "$WAKE_SRC" /root/.local/opt/wake/wake-cli
if [ -e /root/.local/share/wake/wake.db ]; then
  /root/.local/opt/wake/wake-cli refresh
else
  /root/.local/opt/wake/wake-cli index
fi
install -o root -g root -m 0644 "$work/wake-refresh.service" /etc/systemd/system/wake-refresh-root.service
install -o root -g root -m 0644 "$work/wake-refresh.timer" /etc/systemd/system/wake-refresh-root.timer
systemctl daemon-reload
systemctl enable --now wake-refresh-root.timer

echo "== Wake in CT $CT"
pct exec "$CT" -- mkdir -p /root/.local/opt/wake
pct push "$CT" "$WAKE_SRC" /root/.local/opt/wake/wake-cli --user 0 --group 0 --perms 0755
if pct exec "$CT" -- test -e /root/.local/share/wake/wake.db; then
  pct exec "$CT" -- /root/.local/opt/wake/wake-cli refresh
else
  pct exec "$CT" -- /root/.local/opt/wake/wake-cli index
fi
pct push "$CT" "$work/wake-refresh.service" /etc/systemd/system/wake-refresh.service --user 0 --group 0 --perms 0644
pct push "$CT" "$work/wake-refresh.timer" /etc/systemd/system/wake-refresh.timer --user 0 --group 0 --perms 0644
pct exec "$CT" -- systemctl daemon-reload
pct exec "$CT" -- systemctl enable --now wake-refresh.timer
pct exec "$CT" -- /root/.local/opt/wake/wake-cli sessions --agent hermes --limit 5 || true

echo "== sudoers: $SUDOERS"
cat > "$work/sudoers" <<EOF
$USER_NAME ALL=(root) NOPASSWD: $BIN _op --source-only
$USER_NAME ALL=(root) NOPASSWD: /usr/sbin/pct exec $CT -- $BIN _op --source-only
EOF
visudo -cf "$work/sudoers"
install -o root -g root -m 0440 "$work/sudoers" "$SUDOERS"
visudo -c

echo "== check as $USER_NAME"
req='{"protocol":1,"version":"'"$("$BIN" version)"'","op":"rows","past_limit":3}'
printf '%s' "$req" | runuser -u "$USER_NAME" -- sudo -n -u root "$BIN" _op --source-only | head -c 300; echo
printf '%s' "$req" | runuser -u "$USER_NAME" -- sudo -n /usr/sbin/pct exec "$CT" -- "$BIN" _op --source-only | head -c 300; echo

cat <<EOF

== Touch ID unlock: one manual step
1. In 1Password on the Mac, create an SSH key named "rex-rollcall unlock".
   Save its public key on the Mac as ~/.ssh/rex-rollcall-unlock.pub.
2. Append this line to /root/.ssh/authorized_keys on this host, with that public key:

   restrict,command="$BIN _unlock 10m" ssh-ed25519 AAAA...your-public-key... rex-rollcall-unlock

   The key can only run that command: no shell, no forwarding, no pty.
3. sshd must allow root keys. Current setting:
   $(sshd -T 2>/dev/null | grep -i '^permitrootlogin' || echo "unknown (run: sshd -T | grep permitrootlogin)")
   "prohibit-password", "without-password" or "forced-commands-only" all work.
4. Test from the Mac:
   ssh -o IdentitiesOnly=yes -o 'IdentityAgent="~/Library/Group Containers/2BUA8C4S2C.com.1password/t/agent.sock"' \\
       -i ~/.ssh/rex-rollcall-unlock.pub root@$(hostname)
   Touch ID asks, then it prints "source previews unlocked until HH:MM".
   Each unlock and each source preview is logged: journalctl -t rex-rollcall
EOF
