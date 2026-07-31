#!/usr/bin/env bash
# Restore rehearsal against the disposable test database.
#
# Dumps the test database, restores it into a fresh sibling database, and
# verifies that the migration version and task state survive. This is a
# rehearsal only: never point it at real data.
#
# Usage: scripts/restore-rehearsal.sh [container] [source-db]
#   container defaults to llm-broker-test-pg
#   source-db defaults to llm_broker_test
set -euo pipefail

CONTAINER="${1:-llm-broker-test-pg}"
SOURCE_DB="${2:-llm_broker_test}"
TARGET_DB="${SOURCE_DB}_restore"

DUMP="$(mktemp /tmp/llm-broker-restore-XXXXXX.dump)"
trap 'rm -f "$DUMP"; docker exec "$CONTAINER" psql -U postgres -c "DROP DATABASE IF EXISTS ${TARGET_DB}" >/dev/null 2>&1 || true' EXIT

echo "== dump ${SOURCE_DB}"
docker exec "$CONTAINER" pg_dump -U postgres -Fc "$SOURCE_DB" > "$DUMP"

echo "== create ${TARGET_DB}"
docker exec "$CONTAINER" psql -U postgres -c "CREATE DATABASE ${TARGET_DB}" >/dev/null

echo "== restore"
docker exec -i "$CONTAINER" pg_restore -U postgres -d "$TARGET_DB" < "$DUMP"

echo "== verify"
docker exec "$CONTAINER" psql -U postgres -d "$TARGET_DB" -tAc \
  "SELECT 'migration version: ' || max(version_id) FROM goose_db_version"
docker exec "$CONTAINER" psql -U postgres -d "$TARGET_DB" -tAc \
  "SELECT 'tasks by state: ' || state || '=' || count(*) FROM tasks GROUP BY state ORDER BY state"

echo "== rehearsal OK"
