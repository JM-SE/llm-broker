# systemd example units

Parameterized example deployment for a single host. The broker runs as two
separate unprivileged users: the API user cannot read the worker's backend
credential file. Migrations are never run at startup.

## Setup

```bash
sudo useradd --system --home-dir /var/lib/llm-broker-api --shell /usr/sbin/nologin llm-broker-api
sudo useradd --system --home-dir /var/lib/llm-broker-worker --shell /usr/sbin/nologin llm-broker-worker
sudo install -d -o root -g root -m 0755 /etc/llm-broker
sudo install -d -o llm-broker-api -g llm-broker-api /var/lib/llm-broker-api
sudo install -d -o llm-broker-worker -g llm-broker-worker /var/lib/llm-broker-worker
sudo install -m 0640 -o root -g llm-broker-api api.env /etc/llm-broker/
sudo install -m 0640 -o root -g llm-broker-worker worker.env /etc/llm-broker/
sudo install -m 0600 -o root -g root migration.env provisioner.env /etc/llm-broker/
sudo install -m 0644 -o root -g root /path/to/config.json /etc/llm-broker/config.json
sudo install -m 0755 /path/to/llm-broker /usr/local/bin/llm-broker
```

`api.env` and `worker.env` set `LLM_BROKER_CONFIG_FILE=/etc/llm-broker/config.json`
and the runtime-only `LLM_BROKER_DATABASE_URL`. The API and worker files are
separate root-owned mode-0640 files, each readable only by its respective
service group. Put backend credentials only in `worker.env`; never make that
file readable by the API user.
`migration.env` contains `LLM_BROKER_CONFIG_FILE` plus
`LLM_BROKER_MIGRATION_DATABASE_URL`; `provisioner.env` contains
`LLM_BROKER_CONFIG_FILE` plus `LLM_BROKER_PROVISIONER_DATABASE_URL`. Keep both
files root-owned mode 0600 and never put their values in command arguments.

## Migration runbook

Migrations run explicitly, once, before starting services:

```bash
sudo systemd-run --wait --pipe --collect \
  -p EnvironmentFile=/etc/llm-broker/migration.env \
  /usr/local/bin/llm-broker migrate status
# review, then:
sudo systemd-run --wait --pipe --collect \
  -p EnvironmentFile=/etc/llm-broker/migration.env \
  /usr/local/bin/llm-broker migrate up
```

Apply the desired workload policy before starting either long-running unit:

```bash
sudo systemd-run --wait --pipe --collect \
  -p EnvironmentFile=/etc/llm-broker/provisioner.env \
  /usr/local/bin/llm-broker policy validate
sudo systemd-run --wait --pipe --collect \
  -p EnvironmentFile=/etc/llm-broker/provisioner.env \
  /usr/local/bin/llm-broker policy diff
sudo systemd-run --wait --pipe --collect \
  -p EnvironmentFile=/etc/llm-broker/provisioner.env \
  /usr/local/bin/llm-broker policy apply
sudo systemd-run --wait --pipe --collect \
  -p EnvironmentFile=/etc/llm-broker/provisioner.env \
  /usr/local/bin/llm-broker policy status
```

Start the worker first so it owns the execution lock before the API accepts
traffic, then start the API and verify readiness:

```bash
sudo systemctl start llm-broker-worker
sudo systemctl start llm-broker-api
curl -fsS http://127.0.0.1:8010/ready
```

## Files

- `llm-broker-api.service` — HTTP API unit.
- `llm-broker-worker.service` — worker unit (leader + execution).
