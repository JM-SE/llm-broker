#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

for script in \
	scripts/h00-scan-common.sh \
	scripts/check-h00-boundaries.sh \
	scripts/check-h00-artifacts.sh \
	scripts/h00-evidence-init.sh \
	scripts/h00-evidence-finalize.sh \
	scripts/h00-evidence-capture-common.sh \
	scripts/h00-evidence-preflight.sh \
	scripts/h00-evidence-preflight-contract-test.sh; do
	bash -n "$script"
done

expected_scope="$(mktemp)"
actual_scope="$(mktemp)"
scan_fixture="$(mktemp)"
artifact_payload="$(mktemp -d)"
artifact_canary="$(mktemp)"
fake_bin="$(mktemp -d)"
pointer_file="$(mktemp)"
trap 'rm -f -- "$expected_scope" "$actual_scope" "$scan_fixture" "$artifact_canary" "$pointer_file"; rm -rf -- "$artifact_payload" "$fake_bin"; if [[ -n "${LLM_BROKER_TEST_ENV_FILE:-}" ]]; then rm -f -- "$LLM_BROKER_TEST_ENV_FILE"; fi' EXIT
git ls-files | while IFS= read -r path; do
	case "$path" in
	cmd/llm-broker/*|internal/*|migrations/*|openapi.yaml)
		printf '%s\n' "$path"
		;;
	*.go)
		[[ "$path" != */* ]] || continue
		printf '%s\n' "$path"
		;;
	esac
done | sort -u >"$expected_scope"
(source scripts/h00-scan-common.sh; h00_product_scope) | sort -u >"$actual_scope"
cmp -s "$expected_scope" "$actual_scope" || {
	echo "H00 C6 scope contract failed" >&2
	exit 1
}

if grep -Eq 'git ls-files -co' scripts/h00-scan-common.sh; then
	echo "H00 C6 scope/pattern contract failed" >&2
	exit 1
fi

pattern="$(source scripts/h00-scan-common.sh; printf '%s' "$H00_PRODUCT_PATTERN")"
if printf '%s' "$pattern" | grep -Eiq 'q[[:punct:][:space:]]*wen|/home/|0\.0\.0\.0'; then
	echo "H00 C6 product pattern contract failed" >&2
	exit 1
fi
printf 'Role Radar\nY-T/Pipe\nllama-swap\n' | grep -Eiq "$pattern"
if printf 'qwen\n/home/private\n0.0.0.0\n' | grep -Eiq "$pattern"; then
	echo "H00 C6 product pattern contract failed" >&2
	exit 1
fi

printf 'Role Radar\n' >"$scan_fixture"
(source scripts/h00-scan-common.sh; h00_scan_product_files "$scan_fixture" >/dev/null) || {
	echo "H00 C6 case-insensitive scanner contract failed" >&2
	exit 1
}

chmod 600 "$artifact_canary"
cat >"$artifact_canary" <<'EOF'
role_passwords	contract-role-password
client_tokens	contract-client-token
backend_credentials	contract-backend-credential
credential_urls	contract-credential-url
bearer_headers	contract-bearer-header
temporary_paths	/tmp/contract-temporary-path
EOF
printf 'export LLM_BROKER_TEST_ENV_FILE=/tmp/llm-broker-test-env.A1b2C3\n' >"$artifact_payload/leak.txt"
if scripts/check-h00-artifacts.sh --payload "$artifact_payload" --canary "$artifact_canary" >/dev/null 2>&1; then
	echo "H00 C6 temporary-path artifact contract failed" >&2
	exit 1
fi

cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
inspect) exit 1 ;;
run|exec) exit 0 ;;
*) exit 0 ;;
esac
EOF
chmod 700 "$fake_bin/docker"
rm -f -- "$pointer_file"
pointer_output="$(PATH="$fake_bin:$PATH" LLM_BROKER_TEST_PG_NAME=h00-contract-pg \
	scripts/pg-test-up.sh --pointer-file "$pointer_file" 2>/dev/null)"
if [[ "$pointer_output" != "test environment pointer written" ]] ||
	grep -Fq -- '/tmp/llm-broker-test-env' <<<"$pointer_output" ||
	[[ ! -s "$pointer_file" ]]; then
	echo "H00 C6 pointer-output contract failed" >&2
	exit 1
fi
source "$pointer_file"
[[ "$(stat -c '%a' "$LLM_BROKER_TEST_ENV_FILE")" == "600" ]] || {
	echo "H00 C6 pointer environment mode contract failed" >&2
	exit 1
}

if grep -Eq 'scan-status|final-scan-attestation\.json.*manifest' scripts/h00-evidence-finalize.sh; then
	echo "H00 C6 finalizer contract failed" >&2
	exit 1
fi
for output in product-scan.stdout product-scan.stderr secret-private-scan.stdout secret-private-scan.stderr final-scan-attestation.json; do
	grep -Fq -- "$output" scripts/h00-evidence-finalize.sh || {
		echo "H00 C6 finalizer metadata contract failed" >&2
		exit 1
	}
done
