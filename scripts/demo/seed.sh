#!/usr/bin/env bash
# Seed a throwaway vink instance for the README screenshots and the GIF, so they can be taken
# again. Everything lives under DEMO_DIR (default /tmp/vink-demo) on a free port; nothing here
# touches a real instance. `seed.sh up` builds, seeds and leaves the instance running, writing
# $DEMO_DIR/env with the port and the keys for scripts/demo/shots.py and assets/readme/run.tape;
# `seed.sh down` stops it.
#
# What it does: starts a target server on 127.0.0.1:18080 (/healthz answers {"status":"ok"})
# and a webhook receiver on 127.0.0.1:18081, bootstraps org homelab with user demo, applies
# scripts/demo/vink.yaml, backfills 90 days of events and observations with sqlite3 (rows the
# app itself writes, from a fixed seed, with three short incidents), leaves a few admin actions
# in the audit log, and brings the monitors to these states: nightly-backup down with a failed
# run and its body, photo-sync late, dns-home paused, offsite-copy new, everything else up.
set -euo pipefail

DEMO_DIR=${DEMO_DIR:-/tmp/vink-demo}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
VINK_BIN=${VINK_BIN:-$ROOT/bin/vink}
TARGET_PORT=18080
HOOK_PORT=18081
PASSWORD="demo-password-1"

stop() {
  if [ -f "$DEMO_DIR/pids" ]; then
    while read -r pid; do kill "$pid" 2>/dev/null || true; done < "$DEMO_DIR/pids"
    rm -f "$DEMO_DIR/pids"
  fi
}

case "${1:-up}" in
  down) stop; echo "stopped"; exit 0 ;;
  up) ;;
  *) echo "usage: $0 [up|down]" >&2; exit 1 ;;
esac

need() { command -v "$1" >/dev/null || { echo "seed.sh: $1 is needed" >&2; exit 1; }; }
need python3; need sqlite3; need curl

[ -x "$VINK_BIN" ] || (cd "$ROOT" && go build -o "$VINK_BIN" ./cmd/vink)
stop 2>/dev/null || true
rm -rf "$DEMO_DIR" && mkdir -p "$DEMO_DIR"
cp "$VINK_BIN" "$DEMO_DIR/vink"
# port 8080 when it is free, so the addresses in the screenshots read naturally
PORT=$(python3 -c 'import socket
for p in (8080, 8090, 0):
    s = socket.socket()
    try:
        s.bind(("127.0.0.1", p)); print(s.getsockname()[1]); break
    except OSError: pass
    finally: s.close()')
BASE="http://127.0.0.1:$PORT"
DB="$DEMO_DIR/vink.db"
export VINK_DB_PATH="$DB" VINK_SERVER_LISTEN="127.0.0.1:$PORT" VINK_SERVER_BASE_URL="$BASE" \
  VINK_CONFIG="$DEMO_DIR/cli.toml" VINK_SECRETS_KEY_FILE="$DEMO_DIR/secret.key" \
  VINK_SERVER_TRUSTED_PROXIES=127.0.0.1/32 VINK_LOG_FORMAT=json VINK_LOG_LEVEL=warn NO_COLOR=1
# the demo's "job hosts", TEST-NET addresses sent as X-Forwarded-For from the trusted loopback
FROM_NAS="X-Forwarded-For: 192.0.2.10"
FROM_CI="X-Forwarded-For: 192.0.2.5"
cd "$DEMO_DIR"

# the targets the checks hit, and the receiver the webhook channel posts to
python3 - "$TARGET_PORT" "$HOOK_PORT" > "$DEMO_DIR/targets.log" 2>&1 <<'PY' &
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
class Target(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"status":"ok"}' if self.path.startswith("/healthz") else b"<html><body>ok</body></html>"
        self.send_response(200); self.send_header("Content-Type", "application/json" if self.path.startswith("/healthz") else "text/html")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0")); payload = self.rfile.read(n)
        with open("hooks.log", "ab") as f: f.write(payload + b"\n")
        self.send_response(200); self.send_header("Content-Length", "0"); self.end_headers()
    def log_message(self, *a): pass
t = HTTPServer(("127.0.0.1", int(sys.argv[1])), Target); h = HTTPServer(("127.0.0.1", int(sys.argv[2])), Hook)
Thread(target=t.serve_forever, daemon=True).start(); h.serve_forever()
PY
echo $! >> "$DEMO_DIR/pids"

# bootstrap and serve
INIT=$(printf '%s\n' "$PASSWORD" | ./vink admin init --org homelab --user demo --name "Demo Admin" --email demo@example.com --password-stdin --timezone Europe/Amsterdam --json)
KEY=$(printf '%s' "$INIT" | python3 -c 'import sys,json; print(json.load(sys.stdin)["api_key"])')
PING_KEY=$(printf '%s' "$INIT" | python3 -c 'import sys,json; print(json.load(sys.stdin)["ping_key"])')
serve() {
  ./vink serve >> "$DEMO_DIR/serve.log" 2>&1 &
  echo $! >> "$DEMO_DIR/pids"
  for _ in $(seq 1 50); do curl -fsS "$BASE/readyz" >/dev/null 2>&1 && return 0; sleep 0.2; done
  echo "seed.sh: the server did not come up; see $DEMO_DIR/serve.log" >&2; exit 1
}
serve
./vink ctx add demo --server "$BASE" --key "$KEY" >/dev/null
./vink apply -f "$ROOT/scripts/demo/vink.yaml" >/dev/null
echo "applied scripts/demo/vink.yaml"

# 90 days of history, written while the server is stopped: events for the bars and the
# incidents list, observations for the drawers and the sparklines, from a fixed seed
SERVE_PID=$(tail -1 "$DEMO_DIR/pids"); kill "$SERVE_PID"; wait "$SERVE_PID" 2>/dev/null || true
sed -i.bak '$d' "$DEMO_DIR/pids" && rm -f "$DEMO_DIR/pids.bak"
python3 - <<'PY' | sqlite3 -bail "$DB"
import random, time
random.seed(42)
now = int(time.time() * 1000)
DAY = 86_400_000
B32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
def ulid(ms):
    t = ""
    for _ in range(10): t = B32[ms % 32] + t; ms //= 32
    return t + "".join(random.choice(B32) for _ in range(16))
def sql(s): print(s)
def q(v): return "NULL" if v is None else ("'" + str(v).replace("'", "''") + "'")
P = "(SELECT id FROM projects WHERE slug = 'homelab')"
def M(slug): return f"(SELECT id FROM monitors WHERE slug = '{slug}')"
sql(".timeout 5000")
sql("BEGIN;")
# the checks that ran between apply and the stop would make every pull monitor "new" at the
# start of its bar; the history below replaces them
sql(f"DELETE FROM events WHERE at >= {now - 120_000};")
sql(f"DELETE FROM observations WHERE at >= {now - 120_000};")
slugs = ["nightly-backup", "db-dump", "cert-renew", "photo-sync", "offsite-copy", "api-health", "web", "postgres", "dns-home"]
created = now - 92 * DAY
for s in slugs:
    sql(f"UPDATE monitors SET created_at = {created} WHERE slug = '{s}';")
def event(slug, at, frm, to, reason, obs=None):
    i = ulid(at)
    sql(f"INSERT INTO events (id, monitor_id, project_id, at, from_state, to_state, reason, observation_id) VALUES ({q(i)}, {M(slug)}, {P}, {at}, {q(frm)}, {q(to)}, {q(reason)}, {q(obs)});")
    return i
def obs(slug, at, source, signal, ok, latency=None, exit_code=None, duration=None, remote=None, ua=None, detail="{}"):
    i = ulid(at)
    sql(f"INSERT INTO observations (id, monitor_id, project_id, at, source, signal, ok, latency_ms, exit_code, run_id, duration_ms, remote_addr, user_agent, body_ref, detail) VALUES ({q(i)}, {M(slug)}, {P}, {at}, {q(source)}, {q(signal)}, {1 if ok else 0}, {q(latency)}, {q(exit_code)}, NULL, {q(duration)}, {q(remote)}, {q(ua)}, NULL, {q(detail)});")
    return i
def incident(slug, opened, resolved, open_ev, close_ev):
    sql(f"INSERT INTO incidents (id, monitor_id, project_id, opened_at, resolved_at, acked_by, acked_at, open_event_id, close_event_id) VALUES ({q(ulid(opened))}, {M(slug)}, {P}, {opened}, {resolved}, NULL, NULL, {q(open_ev)}, {q(close_ev)});")
# every monitor came up 91 days ago, before the 90-day window starts
for s in slugs:
    event(s, now - 91 * DAY, "new", "up", "")
# three short incidents
def night(slug, days_ago, hour, minute):
    base = now - days_ago * DAY
    d = base - (base % DAY)  # midnight UTC
    return d + (hour * 60 + minute) * 60_000
t0 = night("nightly-backup", 12, 1, 0)        # 03:00 Amsterdam is 01:00 UTC
late = event("nightly-backup", t0, "up", "late", "next due")
down = event("nightly-backup", t0 + 30 * 60_000, "late", "down", "grace over")
up = event("nightly-backup", t0 + 162 * 60_000, "down", "up", "")
incident("nightly-backup", t0 + 30 * 60_000, t0 + 162 * 60_000, down, up)
t1 = night("api-health", 30, 12, 2)
event("api-health", t1, "up", "late", "connection refused")
d1 = event("api-health", t1 + 90_000, "late", "down", "connection refused")
u1 = event("api-health", t1 + 19 * 60_000, "down", "up", "")
incident("api-health", t1 + 90_000, t1 + 19 * 60_000, d1, u1)
t2 = night("web", 55, 7, 10)
event("web", t2, "up", "late", "status 503")
d2 = event("web", t2 + 120_000, "late", "down", "status 503")
u2 = event("web", t2 + 45 * 60_000, "down", "up", "")
incident("web", t2 + 45 * 60_000 - 43 * 60_000, t2 + 45 * 60_000, d2, u2)
# heartbeat history: one ping a night, a few seconds after the schedule
for days in range(90, 0, -1):
    for slug, hour, minute in (("nightly-backup", 1, 0), ("db-dump", 0, 30)):
        at = night(slug, days, hour, minute) + random.randint(40, 400) * 1000
        ok = not (slug == "nightly-backup" and days == 12)
        obs(slug, at, "ping", "exit", ok, exit_code=0 if ok else 1, duration=random.randint(180, 420) * 1000, remote="192.0.2.10", ua="curl/8.4.0")
    if days % 1 == 0:
        obs("cert-renew", night("cert-renew", days, 4, 15) + random.randint(0, 60) * 1000, "ping", "ok", True, remote="192.0.2.11", ua="curl/8.4.0")
    if days % 7 == 0:
        obs("offsite-copy", night("offsite-copy", days, 3, 0) + random.randint(0, 900) * 1000, "ping", "exit", True, exit_code=0, duration=random.randint(1500, 2600) * 1000, remote="192.0.2.12", ua="curl/8.4.0")
# the last 24 hours of checks, every five minutes, for the sparklines
for slug, lo, hi, detail in (("api-health", 8, 24, '{"status":200}'), ("web", 12, 40, '{"status":200}'), ("postgres", 2, 9, "{}")):
    for i in range(288, 0, -1):
        obs(slug, now - i * 5 * 60_000 - random.randint(0, 900), "local", "ok", True, latency=random.randint(lo, hi), detail=detail)
# photo-sync pinged every minute until an hour ago
for i in range(180, 60, -1):
    obs("photo-sync", now - i * 60_000, "ping", "ok", True, remote="192.0.2.20", ua="rclone/v1.68")
# the monitors have been up since their last incident, so the live pings and checks do not
# start them from new; offsite-copy stays new (never pinged)
for s, due in (("nightly-backup", 3600_000), ("db-dump", 3600_000), ("cert-renew", 3600_000), ("photo-sync", 60_000),
               ("api-health", 0), ("web", 0), ("postgres", 0), ("dns-home", 0)):
    sql(f"UPDATE monitors SET state = 'up', state_since = {now - 11 * DAY}, last_obs_at = {now - 3600_000}, last_ok_at = {now - 3600_000}, next_due_at = {now + due} WHERE slug = '{s}';")
sql("COMMIT;")
PY
echo "backfilled 90 days of history"
serve

# admin actions for the audit log: an invite accepted, a role change, a monitor edited by API
JAR="$DEMO_DIR/cookies"
curl -s -c "$JAR" -b "$JAR" -o /dev/null -X POST --data-urlencode "username=demo" --data-urlencode "password=$PASSWORD" --data-urlencode "next=/" "$BASE/login"
CSRF=$(curl -s -b "$JAR" "$BASE/o/homelab/admin/members?invite=1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//; s/"//')
LINK=$(curl -s -b "$JAR" -c "$JAR" -X POST --data-urlencode "_csrf=$CSRF" --data-urlencode "inv_for=Sam" --data-urlencode "inv_role=member" "$BASE/o/homelab/admin/members/invites" | grep -o '/invite/iv_[A-Za-z0-9]*' | head -1)
curl -s -o /dev/null -H "X-Forwarded-For: 192.0.2.7" -X POST --data-urlencode "username=sam" --data-urlencode "display_name=Sam Rivera" --data-urlencode "password=sam-password-1" "$BASE$LINK"
./vink admin user grant sam --org homelab --role admin >/dev/null
curl -s -o /dev/null -H "$FROM_CI" -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{"interval":"45s"}' "$BASE/api/v1/monitors/api-health"
echo "audit log seeded"

# live states
curl -fsS -o /dev/null -H "$FROM_NAS" "$BASE/ping/$PING_KEY/db-dump/0"
curl -fsS -o /dev/null -H "X-Forwarded-For: 192.0.2.11" "$BASE/ping/$PING_KEY/cert-renew"
curl -fsS -o /dev/null -H "X-Forwarded-For: 192.0.2.20" "$BASE/ping/$PING_KEY/photo-sync"
MSG="restic: unable to create lock: repository is already locked by PID 4120"
curl -fsS -o /dev/null -H "$FROM_NAS" -X POST -H "Content-Type: text/plain" --data-binary "$MSG (host nas, repo /mnt/backup)" "$BASE/ping/$PING_KEY/nightly-backup/1?msg=$(python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$MSG")"
for s in api-health web postgres; do ./vink check "$s" >/dev/null; done
./vink pause dns-home >/dev/null
cat > "$DEMO_DIR/backup.sh" <<'SH'
#!/bin/sh
echo "restic backup --repo /mnt/backup"
sleep 1
echo "Files: 1203 new, 88 changed, 48217 unmodified"
echo "snapshot 4f1c2a9b saved"
SH
chmod +x "$DEMO_DIR/backup.sh"
cat > "$DEMO_DIR/env" <<ENV
export VINK_CONFIG="$DEMO_DIR/cli.toml"
export DEMO_BASE="$BASE"
export DEMO_PORT="$PORT"
export DEMO_KEY="$KEY"
export DEMO_PING_KEY="$PING_KEY"
export DEMO_USER="demo"
export DEMO_PASSWORD="$PASSWORD"
export DEMO_DB="$DB"
ENV
echo "waiting 65 s for photo-sync to turn late"
sleep 65
./vink ls
echo
echo "running at $BASE (user demo, password $PASSWORD); $DEMO_DIR/env has the keys"
echo "stop with: $0 down"
