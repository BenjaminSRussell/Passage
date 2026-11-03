#!/usr/bin/env bash
# Wave 10 AC1: seed 50k customers, 20k vendors, 100k items and 10k BOMs into the
# migration pipeline against Postgres, write a reconciliation report, and fail
# if the load takes 30 minutes or more or reconciliation is not 100%.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

NAME="${AC1_PG_NAME:-passage-ac1-pg}"
IMAGE="${AC1_PG_IMAGE:-postgres:17-alpine}"
PASSWORD="${AC1_PG_PASSWORD:-passage}"
PORT="${AC1_PG_PORT:-5432}"
REPORT="${AC1_REPORT:-internal/ac1load/testdata/ac1-reconciliation.json}"
MARKDOWN="${AC1_MARKDOWN:-internal/ac1load/testdata/ac1-reconciliation.md}"

if [[ -z "${DOCKER_HOST:-}" ]] && docker info >/dev/null 2>&1; then
  :
elif [[ -z "${DOCKER_HOST:-}" ]] && docker -H tcp://127.0.0.1:2375 info >/dev/null 2>&1; then
  export DOCKER_HOST=tcp://127.0.0.1:2375
fi

if [[ -z "${PASSAGE_TEST_DATABASE_URL:-}" && -z "${DATABASE_URL:-}" ]]; then
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    docker pull "$IMAGE"
  fi
  if docker ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
    docker start "$NAME" >/dev/null
  else
    docker run -d --name "$NAME" -p "$PORT:5432" \
      -e POSTGRES_PASSWORD="$PASSWORD" \
      -e POSTGRES_DB=passage_ac1 \
      "$IMAGE" >/dev/null
  fi
  for _ in $(seq 1 60); do
    if docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  docker exec "$NAME" pg_isready -U postgres >/dev/null
  export DATABASE_URL="postgres://postgres:${PASSWORD}@127.0.0.1:${PORT}/passage_ac1?sslmode=disable"
fi

mkdir -p "$(dirname "$REPORT")" "$(dirname "$MARKDOWN")"
go run ./cmd/ac1load -report "$REPORT" -markdown "$MARKDOWN"
