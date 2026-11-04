#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

NAME="${AC1_PG_NAME:-passage-ac1-pg}"
IMAGE="${AC1_PG_IMAGE:-postgres:16-alpine}"
PASSWORD="${AC1_PG_PASSWORD:-passage}"
PORT="${AC1_PG_PORT:-55442}"
REPORT="${AC1_REPORT:-internal/ac1load/testdata/ac1-reconciliation.json}"
MARKDOWN="${AC1_MARKDOWN:-internal/ac1load/testdata/ac1-reconciliation.md}"

if [[ -z "${DOCKER_HOST:-}" ]] && docker info >/dev/null 2>&1; then
  :
elif [[ -z "${DOCKER_HOST:-}" ]] && docker -H tcp://127.0.0.1:2375 info >/dev/null 2>&1; then
  export DOCKER_HOST=tcp://127.0.0.1:2375
fi

if [[ -z "${PASSAGE_TEST_DATABASE_URL:-}" || -z "${PASSAGE_TEST_APP_DATABASE_URL:-}" ]]; then
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    docker pull "$IMAGE"
  fi
  WANT="$IMAGE|$PORT|$PASSWORD"
  if docker ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
    HAVE="$(docker inspect -f '{{index .Config.Labels "ac1.config"}}' "$NAME")"
    if [[ "$HAVE" != "$WANT" ]]; then
      docker rm -f "$NAME" >/dev/null
    fi
  fi
  if docker ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
    docker start "$NAME" >/dev/null
  else
    docker run -d --name "$NAME" --label "ac1.config=$WANT" -p "127.0.0.1:$PORT:5432" \
      -e POSTGRES_PASSWORD="$PASSWORD" \
      -e POSTGRES_DB=passage_ac1 \
      "$IMAGE" >/dev/null
  fi
  for _ in $(seq 1 60); do
    if docker exec "$NAME" pg_isready -U postgres -d passage_ac1 >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  docker exec "$NAME" pg_isready -U postgres -d passage_ac1 >/dev/null
  docker exec -e PGPASSWORD="$PASSWORD" "$NAME" psql -U postgres -d passage_ac1 -v ON_ERROR_STOP=1 -q -c \
    "do \$\$ begin if not exists (select 1 from pg_roles where rolname = 'passage_app') then create role passage_app login nosuperuser nobypassrls; end if; end \$\$; alter role passage_app password '${PASSWORD}';"
  export PASSAGE_TEST_DATABASE_URL="postgres://postgres:${PASSWORD}@127.0.0.1:${PORT}/passage_ac1?sslmode=disable"
  export PASSAGE_TEST_APP_DATABASE_URL="postgres://passage_app:${PASSWORD}@127.0.0.1:${PORT}/passage_ac1?sslmode=disable"
fi

COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
  COMMIT="${COMMIT}-dirty"
fi

mkdir -p "$(dirname "$REPORT")" "$(dirname "$MARKDOWN")"
go run ./cmd/ac1load -report "$REPORT" -markdown "$MARKDOWN" -commit "$COMMIT"
