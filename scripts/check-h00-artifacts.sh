#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"

payload=""
canary=""
manifest=""
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
	--manifest)
		manifest="${2:?missing manifest path}"
		shift 2
		;;
	*)
		echo "usage: scripts/check-h00-artifacts.sh --payload DIR --canary FILE [--manifest PATH]" >&2
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
hit_file="$(mktemp)"
trap 'rm -f -- "$files_file" "$hit_file"' EXIT
h00_repo_absolute_files >"$files_file"
h00_payload_files "$payload" >>"$files_file"
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
done <"$canary"

if [[ -n "$manifest" ]]; then
	mkdir -p "$(dirname "$manifest")"
	payload_sha="$(sha256sum <(h00_payload_files "$payload") | cut -d' ' -f1)"
	repo_sha="$(sha256sum "$files_file" | cut -d' ' -f1)"
	printf 'repo_target_sha256=%s\npayload_target_sha256=%s\nprivate_inventory_sha256=%s\nprivate_inventory_count=%s\ndynamic_inventory_sha256=%s\ndynamic_inventory_count=%s\ndynamic_categories=database_passwords,client_tokens,backend_credentials,credential_urls,bearer_headers,temporary_paths\n' \
		"$repo_sha" "$payload_sha" "$H00_PRIVATE_PATH_PREFIXES_SHA256" "$H00_PRIVATE_PATH_PREFIXES_COUNT" "$dynamic_sha" "$dynamic_count" >"$manifest"
fi
