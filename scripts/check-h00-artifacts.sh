#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"
# shellcheck source=scripts/h00-evidence-capture-common.sh
source "$root/scripts/h00-evidence-capture-common.sh"

payload=""
canary=""
parsed_canary=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--payload)
		payload="${2:?missing payload directory}"
		shift 2
		;;
	--canary)
		canary="${2:?missing dynamic canary file}"
		shift 2
		;;
	--parsed-canary)
		parsed_canary="${2:?missing parsed canary path}"
		shift 2
		;;
	*)
		echo "usage: scripts/check-h00-artifacts.sh --payload DIR --canary FILE [--parsed-canary FILE]" >&2
		exit 2
		;;
	esac
done

[[ -d "$payload" && -f "$canary" ]] || {
	echo "H00 artifact scan requires a payload directory and dynamic canary file" >&2
	exit 2
}
[[ "$(stat -c '%a' "$canary")" == "600" ]] || {
	echo "H00 dynamic canary inventory mode is not 0600" >&2
	exit 1
}
h00_verify_static_inventories
canary="$(realpath "$canary")"

files_file="$(mktemp)"
payload_files_file="$(mktemp)"
hit_file="$(mktemp)"
parsed_owned=0
categories_file="$(mktemp)"
if [[ -z "$parsed_canary" ]]; then
	parsed_canary="$(mktemp)"
	parsed_owned=1
	h00_validate_dynamic_canary "$canary" "$parsed_canary" "$categories_file"
else
	[[ -f "$parsed_canary" && ! -L "$parsed_canary" && "$(stat -c '%a' "$parsed_canary")" == "600" ]] || {
		echo "H00 parsed dynamic canary must be a mode-0600 regular non-symlink" >&2
		exit 1
	}
fi
trap 'rm -f -- "$files_file" "$payload_files_file" "$hit_file" "$categories_file"; if [[ "$parsed_owned" == 1 ]]; then rm -f -- "$parsed_canary"; fi' EXIT
h00_repo_absolute_files >"$files_file"
h00_payload_files "$payload" >"$payload_files_file"
cat "$payload_files_file" >>"$files_file"
sort -u -o "$files_file" "$files_file"

while IFS= read -r file; do
	[[ "$file" == "$canary" ]] && continue
	for secret_pattern in \
		'-----BEGIN (RSA|OPENSSH|EC|PRIVATE) KEY-----' \
		'postgres(ql)?://[^$[:space:]\"]+:[^$[:space:]@\"]+@' \
		'[Aa]uthorization:[[:space:]]*[Bb]earer[[:space:]]+[A-Za-z0-9._~+/=-]+'; do
		set +e
		grep -IEn -- "$secret_pattern" "$file" >"$hit_file" 2>/dev/null
		status=$?
		set -e
		if [[ "$status" -eq 0 ]]; then
			echo "H00 secret/private artifact scan failed" >&2
			exit 1
		elif [[ "$status" -gt 1 ]]; then
			echo "H00 secret/private artifact scan could not inspect its target" >&2
			exit 1
		fi
	done
done <"$files_file"

# Temporary credential/environment-file paths are dynamic canaries even when
# the canary inventory was incomplete. Check the finalized payload separately
# from tracked source so the scanner implementation can describe its own
# temporary-file templates without exempting captured evidence.
temporary_path_pattern='LLM_BROKER_TEST_ENV_FILE[[:space:]]*=[[:space:]]*[^[:space:]]+|/(tmp|var/tmp)/llm-broker-(test-env|test-docker-env)\.[[:alnum:]_-]+'
while IFS= read -r file; do
	set +e
	grep -IEq -- "$temporary_path_pattern" "$file"
	status=$?
	set -e
	if [[ "$status" -eq 0 ]]; then
		echo "H00 temporary credential/environment path scan failed" >&2
		exit 1
	elif [[ "$status" -gt 1 ]]; then
		echo "H00 temporary credential/environment path scan could not inspect its payload" >&2
		exit 1
	fi
done <"$payload_files_file"

for inventory in "$H00_PRIVATE_PATH_PREFIXES_FILE" "$H00_CONCRETE_INTEGRATION_NAMES_FILE"; do
	while IFS= read -r needle; do
		[[ -n "${needle//[[:space:]]/}" ]] || continue
		while IFS= read -r file; do
			[[ "$file" == "$canary" ]] && continue
			if grep -IFq -- "$needle" "$file"; then
				echo "H00 private-path artifact scan matched an approved inventory value" >&2
				exit 1
			fi
		done <"$files_file"
	done <"$inventory"
done

dynamic_sha="$(sha256sum "$canary" | cut -d' ' -f1)"
dynamic_count="$(grep -cve '^[[:space:]]*$' "$canary" || true)"
while IFS= read -r value; do
	[[ -n "${value//[[:space:]]/}" ]] || continue
	while IFS= read -r file; do
		if [[ "$file" == "$canary" ]]; then
			continue
		fi
		if grep -IFq -- "$value" "$file"; then
			echo "H00 dynamic-canary artifact scan failed" >&2
			exit 1
		fi
	done <"$files_file"
	done <"$parsed_canary"
