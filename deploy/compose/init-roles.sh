#!/usr/bin/env bash
set -euo pipefail

: "${LLM_BROKER_PROVISIONER_PASSWORD:?missing provisioner password}"
: "${LLM_BROKER_RUNTIME_PASSWORD:?missing runtime password}"

psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set=ON_ERROR_STOP=1 <<'SQL'
\getenv provisioner_password LLM_BROKER_PROVISIONER_PASSWORD
\getenv runtime_password LLM_BROKER_RUNTIME_PASSWORD
CREATE ROLE llm_broker_provisioner LOGIN PASSWORD :'provisioner_password';
CREATE ROLE llm_broker_runtime LOGIN PASSWORD :'runtime_password';
SQL
