#!/usr/bin/env bash
# Bootstrap the non-migration PostgreSQL roles before an H00 migration on an
# existing database volume. Fresh Compose volumes run init-roles.sh instead.
set -euo pipefail

: "${LLM_BROKER_MIGRATION_DATABASE_URL:?set the migration-owner database URL}"
: "${LLM_BROKER_PROVISIONER_PASSWORD:?set a provisioner password}"
: "${LLM_BROKER_RUNTIME_PASSWORD:?set a runtime password}"
export LLM_BROKER_PROVISIONER_PASSWORD LLM_BROKER_RUNTIME_PASSWORD

case "$LLM_BROKER_MIGRATION_DATABASE_URL" in
  postgres://*|postgresql://*) ;;
  *)
    echo "migration database URL must use postgres:// or postgresql://" >&2
    exit 1
    ;;
esac

connection="${LLM_BROKER_MIGRATION_DATABASE_URL#*://}"
authority="${connection%%/*}"
database_query="${connection#*/}"
if [ "$authority" = "$connection" ] || [ -z "$database_query" ]; then
  echo "migration database URL must include a PostgreSQL authority and database" >&2
  exit 1
fi

userinfo="${authority%@*}"
hostport="${authority#*@}"
if [ "$userinfo" = "$authority" ]; then
  userinfo=""
fi
if [ -n "$userinfo" ]; then
  PGUSER="${userinfo%%:*}"
  if [ "$PGUSER" = "$userinfo" ]; then
    PGPASSWORD=""
  else
    PGPASSWORD="${userinfo#*:}"
  fi
else
  PGUSER="postgres"
  PGPASSWORD=""
fi
if [ "${hostport#\[}" != "$hostport" ]; then
  PGHOST="${hostport%%\]*}"
  PGHOST="${PGHOST#\[}"
  remainder="${hostport#*\]}"
  PGPORT="${remainder#*:}"
else
  PGHOST="${hostport%%:*}"
  PGPORT="${hostport#*:}"
fi
[ "$PGPORT" = "$hostport" ] && PGPORT=5432
PGDATABASE="${database_query%%\?*}"
if [ -z "$PGHOST" ] || [ -z "$PGDATABASE" ]; then
  echo "migration database URL must include a host and database" >&2
  exit 1
fi
query="${database_query#*\?}"
if [ "$query" = "$database_query" ]; then
  PGSSLMODE=""
else
  PGSSLMODE="${query#*sslmode=}"
  PGSSLMODE="${PGSSLMODE%%&*}"
fi
export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE
[ -z "$PGSSLMODE" ] || export PGSSLMODE

psql_safe() {
  psql --dbname="$PGDATABASE" "$@"
}

ensure_role() {
  local role="$1" password_env="$2"
  if [ "$(psql_safe -Atqc "SELECT count(*) FROM pg_roles WHERE rolname = '$role'")" = 0 ]; then
    psql_safe -v ON_ERROR_STOP=1 >/dev/null <<SQL
\getenv role_password $password_env
CREATE ROLE $role LOGIN PASSWORD :'role_password'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
SQL
  else
    psql_safe -v ON_ERROR_STOP=1 >/dev/null <<SQL
\getenv role_password $password_env
ALTER ROLE $role LOGIN PASSWORD :'role_password'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
SQL
  fi
  if [ "$(psql_safe -Atqc "SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member WHERE r.rolname = '$role'")" != 0 ]; then
    echo "role $role has memberships; remove them before H00 bootstrap" >&2
    exit 1
  fi
  # Existing v1 volumes may retain broad grants from the shared-role setup.
  # Remove those grants before 00004 applies the exact least-privilege set.
  psql_safe -v ON_ERROR_STOP=1 >/dev/null <<SQL
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM $role;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM $role;
REVOKE ALL PRIVILEGES ON SCHEMA public FROM $role;
SQL
}

ensure_role llm_broker_provisioner LLM_BROKER_PROVISIONER_PASSWORD
ensure_role llm_broker_runtime LLM_BROKER_RUNTIME_PASSWORD

echo "PostgreSQL H00 roles bootstrapped; run the migration before provisioning policies."
