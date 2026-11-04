#!/usr/bin/env bash
# Timed AC1 run with the real loader (#475): starts numbering, party, product and engineering from this tree on a
# local Postgres, seeds the reference rows the roles need, and runs cmd/realload against them.
#
#   REAL_PG_URL=postgres://postgres@127.0.0.1:5432 ./scripts/real-load.sh [-scale 0.01]
#
# REAL_PG_URL is a superuser on a throwaway server: the script creates (and drops first) the databases
# passage_rl_{numbering,party,product,engineering,migration} and the login role passage_app. Each service applies its
# migrations as the owner, then serves as passage_app (no superuser, no BYPASSRLS), so row-level security is on as in
# production. Extra arguments go to cmd/realload.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SERVICES="$(cd "$ROOT/.." && pwd)"
: "${REAL_PG_URL:?set REAL_PG_URL to a superuser URL on a throwaway Postgres, e.g. postgres://postgres@127.0.0.1:5432}"
WORK="${REAL_WORK:-$(mktemp -d)}"
PORT0="${REAL_PORT_BASE:-18080}"
APP_PW="${REAL_APP_PASSWORD:-passage}"
TENANT=00000000-0000-0000-0000-000000000001
LE=00000000-0000-0000-0000-0000000000e1
TERM_ID=00000000-0000-7000-8000-0000000000f1
PROFILE_ID=00000000-0000-7000-8000-0000000000f2
GROUP_ID=00000000-0000-7000-8000-0000000000f3
VGROUP_ID=00000000-0000-7000-8000-0000000000f4
REPORT="${REAL_REPORT:-$ROOT/internal/ac1load/testdata/real-loader.json}"
MARKDOWN="${REAL_MARKDOWN:-$ROOT/internal/ac1load/testdata/real-loader.md}"

hostpart="${REAL_PG_URL#*://}"; hostpart="${hostpart#*@}"; hostpart="${hostpart%%/*}"
dburl() { echo "${REAL_PG_URL%/}/$1?sslmode=disable"; }
appurl() { echo "postgres://passage_app:${APP_PW}@${hostpart}/$1?sslmode=disable"; }
sql() { psql -X -q -v ON_ERROR_STOP=1 "$(dburl "$1")" -c "$2"; }

pids=()
cleanup() { for p in "${pids[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT

key() { # name -> sets <name>_SEED and <name>_PUB (base64 Ed25519 seed and public key)
  openssl genpkey -algorithm ed25519 -outform DER -out "$WORK/$1.der" 2>/dev/null
  printf -v "${1}_SEED" '%s' "$(tail -c 32 "$WORK/$1.der" | base64 -w0)"
  printf -v "${1}_PUB" '%s' "$(openssl pkey -inform DER -in "$WORK/$1.der" -pubout -outform DER | tail -c 32 | base64 -w0)"
}
key migration
key party

psql -X -q -v ON_ERROR_STOP=1 "$(dburl postgres)" <<SQL
select not exists (select 1 from pg_roles where rolname = 'passage_app') as missing \gset
\if :missing
create role passage_app login nosuperuser nobypassrls;
\endif
alter role passage_app password '${APP_PW}';
SQL
for db in numbering party product engineering migration; do
  sql postgres "drop database if exists passage_rl_$db with (force)"
  sql postgres "create database passage_rl_$db"
done

echo "building services in $WORK" >&2
for svc in numbering party product engineering; do
  (cd "$SERVICES/$svc" && go build -o "$WORK/$svc" "./cmd/$svc")
done
(cd "$ROOT" && go build -o "$WORK/realload" ./cmd/realload)

# start <svc> <n> <database url> [env...]: serve on PORT0+n, wait for /readyz.
start() {
  local svc=$1 n=$2 url=$3; shift 3
  env -i PATH="$PATH" HOME="$HOME" DATABASE_URL="$url" HTTP_ADDR="127.0.0.1:$((PORT0 + n))" ADMIN_ADDR="127.0.0.1:$((PORT0 + 100 + n))" \
    PASSAGE_SERVICE_NAME="$svc" "$@" "$WORK/$svc" >>"$WORK/$svc.log" 2>&1 &
  pids+=($!)
  for _ in $(seq 1 120); do
    curl -fsS "http://127.0.0.1:$((PORT0 + n))/readyz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  echo "$svc did not become ready; see $WORK/$svc.log" >&2
  tail -20 "$WORK/$svc.log" >&2
  exit 1
}
stop_last() { kill "${pids[-1]}"; wait "${pids[-1]}" 2>/dev/null || true; unset 'pids[-1]'; }

# The verifier keeps every token id for its 60 s life, 100k by default: the loader outruns that (about 1,700 calls a
# second into party), and a full cache refuses tokens as invalid (401), so the run raises it.
common=(PASSAGE_ENV=dev "PASSAGE_SERVICE_REPLAY_CACHE_SIZE=${REAL_REPLAY_CACHE:-2000000}")
svc_env() { # verifier for the callers each service accepts
  case $1 in
    numbering) echo "PASSAGE_SERVICE_PUBLIC_KEYS=party=$party_PUB" ;;
    *) echo "PASSAGE_SERVICE_PUBLIC_KEYS=migration=$migration_PUB" ;;
  esac
}
extra_env() {
  case $1 in
    party) echo "NUMBERING_URL=http://127.0.0.1:$PORT0 PASSAGE_SERVICE_SIGNING_KEY=$party_SEED" ;;
  esac
}
n=0
for svc in numbering party product engineering; do
  # shellcheck disable=SC2046
  start "$svc" "$n" "$(dburl "passage_rl_$svc")" MIGRATE_ON_START=true "${common[@]}" $(svc_env "$svc") $(extra_env "$svc")
  stop_last
  # shellcheck disable=SC2046
  start "$svc" "$n" "$(appurl "passage_rl_$svc")" "${common[@]}" $(svc_env "$svc") $(extra_env "$svc")
  n=$((n + 1))
done

# Reference rows the customer roles and party numbers need.
sql passage_rl_numbering "insert into number_sequence (tenant_id, id, code, name, format, legal_entity_id, mode, start_from)
  values ('$TENANT', '00000000-0000-7000-8000-0000000000f9', 'PARTY', 'Parties', 'P-{########}', '$LE', 'continuous', 1)"
sql passage_rl_party "insert into payment_term (tenant_id, id, code, name, net_days) values ('$TENANT', '$TERM_ID', 'NET30', 'Net 30', 30);
  insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ('$TENANT', '$PROFILE_ID', '$LE', 'DOM', 'Domestic')"

for db in numbering party product engineering; do
  sql "passage_rl_$db" "analyze"
done
sql postgres "grant all on database passage_rl_migration to passage_app"
COMMIT="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
[[ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ]] && COMMIT="${COMMIT}-dirty"
NOTE="Postgres $(psql -X -tA "$(dburl postgres)" -c 'show server_version'), services built from this tree on $(nproc) CPUs, one process each, serving as passage_app (RLS on); party numbers from the numbering service; engineering with PASSAGE_ENV=dev (no audit sink, unused by BOM imports); PASSAGE_SERVICE_REPLAY_CACHE_SIZE=${REAL_REPLAY_CACHE:-2000000}"

PASSAGE_SERVICE_NAME=migration PASSAGE_SERVICE_SIGNING_KEY="$migration_SEED" "$WORK/realload" \
  -party "http://127.0.0.1:$((PORT0 + 1))" -product "http://127.0.0.1:$((PORT0 + 2))" -engineering "http://127.0.0.1:$((PORT0 + 3))" \
  -tenant "$TENANT" -legal-entity "$LE" -payment-term "$TERM_ID" -posting-profile "$PROFILE_ID" -customer-group "$GROUP_ID" -vendor-group "$VGROUP_ID" \
  -xref-db "$(appurl passage_rl_migration)" -xref-owner-db "$(dburl passage_rl_migration)" -commit "$COMMIT" -note "$NOTE" -report "$REPORT" -markdown "$MARKDOWN" "$@"
