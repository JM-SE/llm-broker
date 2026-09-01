#!/usr/bin/env bash
# Creates the disposable, loopback-only PostgreSQL used by the P03+
# integration tests. It never touches existing application databases.
#
# Usage: scripts/pg-test-up.sh
# Writes a mode-0600 environment file for the migration-owner, provisioner,
# and runtime URLs, then prints only a shell assignment for that file. Destroy
# with scripts/pg-test-down.sh.
set -euo pipefail

NAME="${LLM_BROKER_TEST_PG_NAME:-llm-broker-test-pg}"
PORT="${LLM_BROKER_TEST_PG_PORT:-55432}"
IMAGE="${LLM_BROKER_TEST_PG_IMAGE:-postgres:17}"
DB="llm_broker_test"
POINTER_FILE=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --pointer-file)
      POINTER_FILE="${2:?missing pointer file path}"
      shift 2
      ;;
    *)
      echo "usage: scripts/pg-test-up.sh [--pointer-file PATH]" >&2
      exit 2
      ;;
  esac
done

if docker inspect "$NAME" >/dev/null 2>&1; then
  echo "container $NAME already exists; destroy it first with scripts/pg-test-down.sh" >&2
  exit 1
fi

ADMIN_PASSWORD="$(openssl rand -hex 16)"
RUNTIME_PASSWORD="$(openssl rand -hex 16)"
PROVISIONER_PASSWORD="$(openssl rand -hex 16)"
DOCKER_ENV_FILE="$(mktemp "${TMPDIR:-/tmp}/llm-broker-test-docker-env.XXXXXX")"
trap 'rm -f "$DOCKER_ENV_FILE"' EXIT
(umask 077; cat >"$DOCKER_ENV_FILE" <<EOF
POSTGRES_PASSWORD=$ADMIN_PASSWORD
POSTGRES_DB=$DB
EOF
)

docker run -d \
  --name "$NAME" \
  --env-file "$DOCKER_ENV_FILE" \
  -p "127.0.0.1:${PORT}:5432" \
  "$IMAGE" >/dev/null
rm -f "$DOCKER_ENV_FILE"

echo "waiting for PostgreSQL on 127.0.0.1:${PORT}..." >&2
database_ready=0
for _ in $(seq 1 30); do
  if docker exec "$NAME" psql -U postgres -d "$DB" -Atqc 'SELECT 1' >/dev/null 2>&1; then
    database_ready=1
    break
  fi
  sleep 1
done
if [ "$database_ready" -ne 1 ]; then
  echo "PostgreSQL did not become ready" >&2
  exit 1
fi

echo "disposable test database ready" >&2
roles_ready=0
for _ in $(seq 1 10); do
  if docker exec -i "$NAME" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
    >/dev/null 2>&1 <<SQL
CREATE ROLE llm_broker_provisioner LOGIN PASSWORD '${PROVISIONER_PASSWORD}';
CREATE ROLE llm_broker_runtime LOGIN PASSWORD '${RUNTIME_PASSWORD}';
SQL
  then
    roles_ready=1
    break
  fi
  sleep 1
done
if [ "$roles_ready" -ne 1 ]; then
  echo "PostgreSQL accepted readiness but did not accept role setup" >&2
  exit 1
fi
ENV_FILE="${LLM_BROKER_TEST_ENV_FILE:-$(mktemp "${TMPDIR:-/tmp}/llm-broker-test-env.XXXXXX")}"
(umask 077; cat >"$ENV_FILE" <<EOF
export TEST_DATABASE_URL="postgres://postgres:${ADMIN_PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
export TEST_DATABASE_PROVISIONER_URL="postgres://llm_broker_provisioner:${PROVISIONER_PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
export TEST_DATABASE_RUNTIME_URL="postgres://llm_broker_runtime:${RUNTIME_PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
EOF
)
chmod 600 "$ENV_FILE"
assignment="$(printf 'export LLM_BROKER_TEST_ENV_FILE=%q\n' "$ENV_FILE")"
if [ -n "$POINTER_FILE" ]; then
  (umask 077; printf '%s' "$assignment" >"$POINTER_FILE")
  chmod 600 "$POINTER_FILE"
  printf 'test environment pointer written\n'
else
  printf '%s' "$assignment"
fi
