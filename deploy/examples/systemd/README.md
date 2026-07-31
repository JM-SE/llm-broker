# systemd example units

Parameterized example deployment for a single host. The broker runs as a
dedicated unprivileged user with separate API and worker units, hardening,
and explicit environment files. Migrations are never run at startup.

## Setup

```bash
sudo useradd --system --home-dir /var/lib/llm-broker --shell /usr/sbin/nologin llm-broker
sudo install -d -o llm-broker -g llm-broker /etc/llm-broker /var/lib/llm-broker
sudo install -m 0640 -o root -g llm-broker api.env worker.env /etc/llm-broker/
sudo install -m 0640 -o root -g llm-broker /path/to/config.json /etc/llm-broker/config.json
sudo install -m 0755 /path/to/llm-broker /usr/local/bin/llm-broker
```

`api.env` / `worker.env` set `LLM_BROKER_CONFIG_FILE=/etc/llm-broker/config.json`
and `LLM_BROKER_DATABASE_URL=postgres://...` (secret, root-owned 0640).

## Migration runbook

Migrations run explicitly, once, before starting services:

```bash
sudo systemd-run --wait --pipe --collect \
  -E LLM_BROKER_CONFIG_FILE=/etc/llm-broker/config.json \
  -E LLM_BROKER_DATABASE_URL="$(grep -oP '(?<=LLM_BROKER_DATABASE_URL=).*' /etc/llm-broker/api.env)" \
  /usr/local/bin/llm-broker migrate status
# review, then:
sudo systemd-run ... /usr/local/bin/llm-broker migrate up
```

Then start both units and verify readiness:

```bash
sudo systemctl start llm-broker-api llm-broker-worker
curl -fsS http://127.0.0.1:8010/ready
```

## Files

- `llm-broker-api.service` — HTTP API unit.
- `llm-broker-worker.service` — worker unit (leader + execution).
