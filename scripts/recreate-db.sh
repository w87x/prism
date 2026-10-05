#!/usr/bin/env bash
# Recreate PRISM's database from the current schema while KEEPING how PRISM is wired up: API keys, providers,
# models, mail accounts, MCP servers, skill hubs, tool settings, plugins, settings, document sources and Telegram
# topics. Everything PRISM produced (agents, tasks, chats, memory, trackers, knowledge pages, …) is dropped.
#
# Why not onboarding's "start over"? That empties tables but keeps the OLD schema; after the schema changed (new
# columns / tables in the base migrations) the new build needs a fresh database.
#
#   scripts/recreate-db.sh                 dry run: shows what would happen, changes nothing
#   scripts/recreate-db.sh --apply         does it (asks you to type the database name first)
#   scripts/recreate-db.sh --resume DIR    only (re)load the kept tables from an earlier run's backup folder into the
#                                          already recreated database — if the restore step failed, fix the cause and run this
#   options: --home DIR (PRISM_HOME, default ~/.prism)   --bin PATH (new prism binary, default ./bin/prism)   --yes
#
# Steps: refuse if PRISM is running → full backup (pg_dump -Fc) + data-only dump of the kept tables → drop and
# create the database → start the new binary once (migrates + seeds Atlas and the maintenance agents) → stop it →
# load the kept tables → forget the onboarding "done" flag so onboarding offers to recreate your specialists.
# Needs psql and pg_dump on PATH. Passwords are never printed.
set -euo pipefail

APPLY=0; YES=0; RESUME=""
HOME_DIR="${PRISM_HOME:-$HOME/.prism}"
BIN="./bin/prism"
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1 ;;
    --yes) YES=1 ;;
    --resume) RESUME="$2"; APPLY=1; shift ;;
    --home) HOME_DIR="$2"; shift ;;
    --bin) BIN="$2"; shift ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

KEEP=(settings providers provider_keys models model_lists mail_accounts mcp_servers skill_hubs tool_settings plugins doc_sources telegram_topics)

command -v psql >/dev/null && command -v pg_dump >/dev/null || { echo "psql and pg_dump are required" >&2; exit 1; }
[ -f "$HOME_DIR/config.json" ] || { echo "no config at $HOME_DIR/config.json (use --home)" >&2; exit 1; }

# connection details from the config; the password stays in the environment, never on the command line
eval "$(python3 - "$HOME_DIR/config.json" <<'PY'
import json, re, shlex, sys
from urllib.parse import urlsplit, urlunsplit
c = json.load(open(sys.argv[1]))
dsn = c["dsn"]
u = urlsplit(dsn)
db = u.path.lstrip("/")
admin = urlunsplit((u.scheme, u.netloc, "/postgres", u.query, ""))
print("DSN=%s" % shlex.quote(dsn))
print("ADMIN_DSN=%s" % shlex.quote(admin))
print("DBNAME=%s" % shlex.quote(db))
print("TARGET=%s" % shlex.quote("%s@%s/%s" % (u.username or "?", u.hostname or "?", db)))
print("LISTEN=%s" % shlex.quote(c.get("listen") or "127.0.0.1:7777"))
PY
)"

q() { psql "$DSN" -Atqc "$1"; }
PORT="${LISTEN##*:}"

if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "Something is listening on port $PORT — stop PRISM first." >&2; exit 1
fi

restore() { # $1 = backup folder holding keep.sql and count.<table> files
  local out="$1" t ids
  PRESENT=(); for f in "$out"/count.*; do PRESENT+=("${f##*/count.}"); done
  echo "Restoring the kept tables …"
  # only tables whose id is a serial have a sequence to bring up to date (checked one table at a time: a table
  # without an id column must never reach pg_get_serial_sequence)
  ids=""
  for t in "${PRESENT[@]}"; do
    if [ "$(psql "$DSN" -Atqc "select count(*) from information_schema.columns where table_schema='public' and table_name='$t' and column_name='id'")" = "1" ] &&
       [ -n "$(psql "$DSN" -Atqc "select pg_get_serial_sequence('public.$t','id')")" ]; then ids="$ids $t"; fi
  done
  {
    echo "SET session_replication_role = replica;"   # kept tables reference each other: load them in any order
    echo "BEGIN;"
    for t in "${PRESENT[@]}"; do echo "DELETE FROM $t;"; done
    echo "\\i $out/keep.sql"
    echo "SET search_path TO public;"   # pg_dump output empties the search path
    for t in "${PRESENT[@]}"; do
      if [[ " $ids " == *" $t "* ]]; then
        echo "SELECT setval(pg_get_serial_sequence('public.$t','id'), GREATEST(1,(SELECT COALESCE(max(id),1) FROM $t)));"
      fi
    done
    echo "DELETE FROM settings WHERE key='onboarding';"  # onboarding opens again: recreate specialists with full toolsets
    echo "COMMIT;"
  } > "$out/restore.sql"
  if ! psql "$DSN" -v ON_ERROR_STOP=1 -q -f "$out/restore.sql" >/dev/null; then
    echo "restoring failed (nothing was half-applied: it is one transaction). Your old data is safe in $out." >&2
    echo "Fix the cause and run:  scripts/recreate-db.sh --home $HOME_DIR --resume $out" >&2
    echo "or put the whole old database back: pg_restore -d \"\$ADMIN_DSN\" --create --clean $out/full.dump" >&2
    exit 1
  fi
  OUT="$out"
}

echo "Database: $TARGET"
echo "Kept tables (rows):"
PRESENT=()
for t in "${KEEP[@]}"; do
  if [ "$(q "select to_regclass('public.$t') is not null")" = "t" ]; then
    PRESENT+=("$t"); printf '  %-18s %s\n' "$t" "$(q "select count(*) from $t")"
  fi
done
[ ${#PRESENT[@]} -gt 0 ] || { echo "none of the kept tables exist: nothing to keep — is this the right database?" >&2; exit 1; }
echo "Dropped: everything else (agents, tasks, chats, memory, trackers, knowledge pages, artifacts rows, logs…)."

if [ "$APPLY" -ne 1 ]; then
  echo; echo "Dry run — nothing was changed. Re-run with --apply to recreate the database."; exit 0
fi

if [ -n "$RESUME" ]; then
  [ -s "$RESUME/keep.sql" ] || { echo "$RESUME has no keep.sql" >&2; exit 1; }
  restore "$RESUME"
  echo "Done: the kept tables were loaded from $RESUME."
  exit 0
fi

[ -x "$BIN" ] || { echo "the new prism binary is not at $BIN (make build, or pass --bin)" >&2; exit 1; }
if [ "$YES" -ne 1 ]; then
  printf 'Type the database name (%s) to drop and recreate it: ' "$DBNAME"
  read -r ans; [ "$ans" = "$DBNAME" ] || { echo "not confirmed" >&2; exit 1; }
fi

STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="$HOME_DIR/backups/$STAMP"
mkdir -p "$OUT"
echo "Backing up to $OUT …"
pg_dump "$DSN" -Fc -f "$OUT/full.dump"
TARGS=(); for t in "${PRESENT[@]}"; do TARGS+=(-t "public.$t"); done
pg_dump "$DSN" --data-only --column-inserts "${TARGS[@]}" -f "$OUT/keep.sql"
[ -s "$OUT/full.dump" ] && [ -s "$OUT/keep.sql" ] || { echo "backup is empty — aborting before touching the database" >&2; exit 1; }
for t in "${PRESENT[@]}"; do q "select count(*) from $t" > "$OUT/count.$t"; done

echo "Dropping and creating $DBNAME …"
psql "$ADMIN_DSN" -qc "DROP DATABASE \"$DBNAME\" WITH (FORCE)" -c "CREATE DATABASE \"$DBNAME\""

echo "Starting the new build once to create the schema and seed the built-in agents …"
LOG="$OUT/first-start.log"
PRISM_HOME="$HOME_DIR" "$BIN" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 1 90); do
  grep -q "listening on" "$LOG" 2>/dev/null && break
  kill -0 "$PID" 2>/dev/null || { echo "the new build exited — see $LOG. Your backup: $OUT/full.dump" >&2; exit 1; }
  sleep 1
done
grep -q "listening on" "$LOG" || { kill "$PID" 2>/dev/null; echo "the new build did not start in 90s — see $LOG. Your backup: $OUT/full.dump" >&2; exit 1; }
kill "$PID"; wait "$PID" 2>/dev/null || true

restore "$OUT"

echo "Done. Kept tables, before → after:"
for t in "${PRESENT[@]}"; do printf '  %-18s %s → %s\n' "$t" "$(cat "$OUT/count.$t")" "$(q "select count(*) from $t")"; done
echo "Backup: $OUT (full.dump is the whole old database)."
echo "Artifact files under the data dir still exist but are no longer listed — delete them if you do not want them."
echo "Start PRISM and finish onboarding to recreate your specialists."
