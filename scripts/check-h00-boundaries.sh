#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"

manifest=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--manifest)
		manifest="${2:?missing manifest path}"
		shift 2
		;;
	*)
		 echo "usage: scripts/check-h00-boundaries.sh [--manifest PATH]" >&2
		exit 2
		;;
	esac
done

h00_verify_static_inventories
scope_file="$(mktemp)"
hit_file="$(mktemp)"
trap 'rm -f -- "$scope_file" "$hit_file"' EXIT
h00_product_scope | sort -u >"$scope_file"
mapfile -t files <"$scope_file"

if ((${#files[@]} > 0)); then
	set +e
	h00_scan_product_files "${files[@]}" >"$hit_file" 2>/dev/null
	status=$?
	set -e
	if [[ "$status" -eq 0 ]]; then
		echo "H00 product-core boundary scan failed" >&2
		exit 1
	elif [[ "$status" -gt 1 ]]; then
		echo "H00 product-core boundary scan could not inspect its frozen scope" >&2
		exit 1
	fi

	for inventory in "$H00_CONCRETE_INTEGRATION_NAMES_FILE" "$H00_PRIVATE_PATH_PREFIXES_FILE"; do
		while IFS= read -r needle; do
			[[ -n "${needle//[[:space:]]/}" ]] || continue
			if grep -IFq -- "$needle" "${files[@]}"; then
				echo "H00 product-core boundary scan matched an approved inventory value" >&2
				exit 1
			fi
		done <"$inventory"
	done
fi

if [[ -n "$manifest" ]]; then
	mkdir -p "$(dirname "$manifest")"
	scope_sha="$(sha256sum "$scope_file" | cut -d' ' -f1)"
	pattern_sha="$(h00_sha256_text "$H00_PRODUCT_PATTERN")"
	printf 'scope_sha256=%s\npattern_sha256=%s\nconcrete_inventory_sha256=%s\nconcrete_inventory_count=%s\n' \
		"$scope_sha" "$pattern_sha" "$H00_CONCRETE_INTEGRATION_NAMES_SHA256" "$H00_CONCRETE_INTEGRATION_NAMES_COUNT" >"$manifest"
fi
