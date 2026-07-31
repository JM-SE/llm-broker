#!/usr/bin/env bash
# Destroys the disposable test PostgreSQL created by scripts/pg-test-up.sh.
set -euo pipefail

NAME="${LLM_BROKER_TEST_PG_NAME:-llm-broker-test-pg}"

if docker inspect "$NAME" >/dev/null 2>&1; then
  docker rm -f "$NAME" >/dev/null
  echo "removed disposable test database $NAME"
else
  echo "no container named $NAME" >&2
fi
