#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

for script in \
	scripts/h00-scan-common.sh \
	scripts/check-h00-boundaries.sh \
	scripts/check-h00-artifacts.sh \
	scripts/h00-evidence-init.sh \
	scripts/h00-evidence-finalize.sh; do
	bash -n "$script"
done

expected_scope="$(mktemp)"
actual_scope="$(mktemp)"
trap 'rm -f -- "$expected_scope" "$actual_scope"' EXIT
git ls-files | while IFS= read -r path; do
	case "$path" in
	cmd/llm-broker/*|internal/*|migrations/*|openapi.yaml|*.go)
		if [[ "$path" == *.go && "$path" == */* ]]; then
			[[ "$path" == cmd/llm-broker/* || "$path" == internal/* ]] || continue
		fi
		printf '%s\n' "$path"
		;;
	esac
done | sort -u >"$expected_scope"
(source scripts/h00-scan-common.sh; h00_product_scope) | sort -u >"$actual_scope"

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
