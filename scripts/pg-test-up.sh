#!/usr/bin/env bash
# Creates the disposable, loopback-only PostgreSQL used by the P03+
# integration tests. It never touches existing application databases.
#
# Usage: scripts/pg-test-up.sh
# Prints the export lines for TEST_DATABASE_URL and
# TEST_DATABASE_RUNTIME_URL. Destroy with scripts/pg-test-down.sh.
set -euo pipefail

NAME="${LLM_BROKER_TEST_PG_NAME:-llm-broker-test-pg}"
PORT="${LLM_BROKER_TEST_PG_PORT:-55432}"
IMAGE="${LLM_BROKER_TEST_PG_IMAGE:-postgres:17}"
DB="llm_broker_test"

if docker inspect "$NAME" >/dev/null 2>&1; then
  echo "container $NAME already exists; destroy it first with scripts/pg-test-down.sh" >&2
  exit 1
fi

ADMIN_PASSWORD="$(openssl rand -hex 16)"
RUNTIME_PASSWORD="$(openssl rand -hex 16)"

docker run -d \
  --name "$NAME" \
  -e POSTGRES_PASSWORD="$ADMIN_PASSWORD" \
  -e POSTGRES_DB="$DB" \
  -p "127.0.0.1:${PORT}:5432" \
  "$IMAGE" >/dev/null

echo "waiting for PostgreSQL on 127.0.0.1:${PORT}..."
for _ in $(seq 1 30); do
  if docker exec "$NAME" pg_isready -U postgres -d "$DB" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! docker exec "$NAME" pg_isready -U postgres -d "$DB" >/dev/null 2>&1; then
  echo "PostgreSQL did not become ready" >&2
  exit 1
fi

echo "disposable test database ready"
echo
echo "export TEST_DATABASE_URL=\"postgres://postgres:${ADMIN_PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable\""
echo "export TEST_DATABASE_RUNTIME_URL=\"postgres://llm_broker_runtime:${RUNTIME_PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable\""
