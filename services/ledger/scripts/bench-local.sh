#!/usr/bin/env bash
# Local AC4 proxy benchmark (issue #476): sustained journal posting and trial
# balance reads against Postgres 16. Not AC4 itself, which runs on the dev
# cluster (#11, #213).
#
# With PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL set, it uses
# that database. Otherwise it creates a throwaway Postgres 16 cluster with
# pg_createcluster (Debian/Ubuntu postgresql-common, run as root) on a free
# port and drops it on exit.
#
# Knobs (environment): PASSAGE_LEDGER_BENCH_SECONDS (30), _WARMUP_SECONDS (5),
# _WORKERS (8), _LEDGERS (4), _LINES (10 lines per entry), _READERS (2),
# _READ_INTERVAL_MS (250), _MIN_LPS and _MAX_P99_MS (fail when the slowest
# one-second window is below / the posting p99 is above), _OUT (JSON report
# path), BENCH_POOL_CONNS (app pool size on a throwaway cluster; default
# workers + readers).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

POOL="${BENCH_POOL_CONNS:-$(( ${PASSAGE_LEDGER_BENCH_WORKERS:-8} + ${PASSAGE_LEDGER_BENCH_READERS:-2} ))}"

if [[ -z "${PASSAGE_TEST_DATABASE_URL:-}" || -z "${PASSAGE_TEST_APP_DATABASE_URL:-}" ]]; then
  command -v pg_createcluster >/dev/null || { echo "pg_createcluster not found; set the PASSAGE_TEST_*_DATABASE_URL variables" >&2; exit 2; }
  [[ "$(id -u)" == 0 ]] || { echo "creating a cluster needs root; set the PASSAGE_TEST_*_DATABASE_URL variables instead" >&2; exit 2; }
  NAME="${BENCH_PG_NAME:-ledgerbench$$}"
  PORT="${BENCH_PG_PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')}"
  PASSWORD=bench
  cleanup() { pg_dropcluster --stop 16 "$NAME" >/dev/null 2>&1 || true; }
  trap cleanup EXIT
  pg_createcluster 16 "$NAME" -p "$PORT" --encoding UTF8 --locale C.UTF-8 >/dev/null
  pg_ctlcluster 16 "$NAME" start
  runuser -u postgres -- psql -p "$PORT" -q -v ON_ERROR_STOP=1 <<SQL
alter role postgres password '$PASSWORD';
create role passage_app login nosuperuser nobypassrls password '$PASSWORD';
SQL
  export PASSAGE_TEST_DATABASE_URL="postgres://postgres:${PASSWORD}@127.0.0.1:${PORT}/postgres?sslmode=disable"
  export PASSAGE_TEST_APP_DATABASE_URL="postgres://passage_app:${PASSWORD}@127.0.0.1:${PORT}/postgres?sslmode=disable&pool_max_conns=${POOL}"
  echo "== throwaway cluster 16/$NAME on port $PORT (dropped on exit)"
fi

export PASSAGE_LEDGER_BENCH=1
go test ./internal/store/ -run '^TestPostgres_AC4Proxy_' -count=1 -v -timeout 30m
