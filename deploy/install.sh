#!/usr/bin/env bash
# Runs on the server, called by .github/workflows/deploy.yml after it has
# copied a release to /opt/pingcord/release. Swaps in the new binary, rolls
# back if it doesn't become healthy, then hands pingcord's site to Caddy.
set -euo pipefail

APP_DIR=/opt/pingcord
RELEASE="$APP_DIR/release"
SITE=/etc/caddy/sites/pingcord.caddy
LISTEN_ADDR=127.0.0.1:8090 # matches deploy/pingcord.service

cd "$APP_DIR"

# The unit needs root to install, so it's done by hand (deploy/README.md).
if ! cmp -s "$RELEASE/pingcord.service" /etc/systemd/system/pingcord.service; then
  echo "::warning::deploy/pingcord.service differs from the installed unit. Install it:" \
    "sudo cp $RELEASE/pingcord.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl restart pingcord"
fi

healthy() {
  for _ in $(seq 1 20); do
    if LISTEN_ADDR=$LISTEN_ADDR "$RELEASE/pingcord" -healthcheck 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# 1. Binary: keep the running one as pingcord.prev for rollback.
install -m 755 "$RELEASE/pingcord" pingcord.new
if [ -f pingcord ]; then cp -p pingcord pingcord.prev; fi
mv -f pingcord.new pingcord
sudo -n systemctl restart pingcord

if ! healthy; then
  echo "::error::pingcord did not become healthy; rolling back"
  journalctl -u pingcord -n 40 --no-pager || true
  if [ -f pingcord.prev ]; then
    mv -f pingcord.prev pingcord
    sudo -n systemctl restart pingcord
  fi
  exit 1
fi
echo "pingcord $("$APP_DIR/pingcord" -version) is healthy"

# 2. Caddy site. A reload with a bad config fails and Caddy keeps the old
# one running, so restore our previous file in that case.
previous=""
if [ -f "$SITE" ]; then
  previous=$(mktemp)
  cp "$SITE" "$previous"
fi
cp "$RELEASE/pingcord.caddy" "$SITE"
if ! sudo -n systemctl reload-or-restart caddy; then
  echo "::error::Caddy rejected the new site config; restoring the previous one"
  if [ -n "$previous" ]; then cp "$previous" "$SITE"; else rm -f "$SITE"; fi
  sudo -n systemctl reload-or-restart caddy || true
  exit 1
fi
echo "caddy reloaded"
