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
#
# Only the named disposable test container and llm_broker_test* databases are
# accepted. This guard is intentionally conservative: the rehearsal must not
# be usable against an application or operator database.
set -euo pipefail
umask 077

CONTAINER="${1:-llm-broker-test-pg}"
SOURCE_DB="${2:-llm_broker_test}"

case "$CONTAINER" in
  llm-broker-test-pg|llm-broker-test-pg-*) ;;
  *)
    echo "restore rehearsal: refusing non-disposable container" >&2
    exit 1
    ;;
esac
case "$SOURCE_DB" in
  llm_broker_test|llm_broker_test_*) ;;
  *)
    echo "restore rehearsal: refusing non-disposable source database" >&2
    exit 1
    ;;
esac

# BASHPID makes repeated rehearsals safe even if a stale target was left by an
# interrupted invocation. The identifier is validated before it is used in SQL.
TARGET_DB="${SOURCE_DB}_restore_${BASHPID}"
if [[ ! "$TARGET_DB" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]; then
  echo "restore rehearsal: invalid disposable target database name" >&2
  exit 1
fi

DUMP="$(mktemp /tmp/llm-broker-restore-XXXXXX.dump)"

cleanup() {
  rm -f -- "$DUMP"
  docker exec "$CONTAINER" psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
    -c "DROP DATABASE IF EXISTS \"$TARGET_DB\"" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

psql_value() {
  docker exec "$CONTAINER" psql -U postgres -d "$1" -v ON_ERROR_STOP=1 -Atqc "$2"
}

echo "== dump disposable source"
docker exec "$CONTAINER" pg_dump -U postgres -Fc -d "$SOURCE_DB" >"$DUMP"

SOURCE_VERSION="$(psql_value "$SOURCE_DB" "SELECT COALESCE(max(version_id), 0) FROM goose_db_version")"
if [[ ! "$SOURCE_VERSION" =~ ^[0-9]+$ ]] || [ "$SOURCE_VERSION" -le 0 ]; then
  echo "restore rehearsal: source migration version is not a positive integer" >&2
  exit 1
fi
SOURCE_TASK_COUNTS="$(psql_value "$SOURCE_DB" "SELECT COALESCE(string_agg(state || '=' || task_count::text, E'\\n' ORDER BY state), '') FROM (SELECT state, count(*) AS task_count FROM tasks GROUP BY state) counts")"

echo "== create disposable restore"
docker exec "$CONTAINER" psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c "DROP DATABASE IF EXISTS \"$TARGET_DB\"" >/dev/null 2>&1
docker exec "$CONTAINER" psql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c "CREATE DATABASE \"$TARGET_DB\"" >/dev/null

echo "== restore"
docker exec -i "$CONTAINER" pg_restore -U postgres -d "$TARGET_DB" <"$DUMP"

echo "== assert restored catalog state"
RESTORED_VERSION="$(psql_value "$TARGET_DB" "SELECT COALESCE(max(version_id), 0) FROM goose_db_version")"
if [ "$RESTORED_VERSION" != "$SOURCE_VERSION" ]; then
  echo "restore rehearsal: restored migration version differs from source" >&2
  exit 1
fi
RESTORED_TASK_COUNTS="$(psql_value "$TARGET_DB" "SELECT COALESCE(string_agg(state || '=' || task_count::text, E'\\n' ORDER BY state), '') FROM (SELECT state, count(*) AS task_count FROM tasks GROUP BY state) counts")"
if [ "$RESTORED_TASK_COUNTS" != "$SOURCE_TASK_COUNTS" ]; then
  echo "restore rehearsal: restored task-state counts differ from source" >&2
  exit 1
fi

echo "== rehearsal OK"
