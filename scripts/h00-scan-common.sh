#!/usr/bin/env bash

# Shared, side-effect-limited helpers for the H00 product and evidence scans.
# This file is sourced by the small scan/finalization entrypoints; it is not a
# standalone acceptance command.

H00_CONCRETE_INTEGRATION_NAMES_FILE="${H00_CONCRETE_INTEGRATION_NAMES_FILE:-/home/jmse/homelab/ai/projects/llm-broker/task-context/H00-concrete-integration-names.txt}"
H00_PRIVATE_PATH_PREFIXES_FILE="${H00_PRIVATE_PATH_PREFIXES_FILE:-/home/jmse/homelab/ai/projects/llm-broker/task-context/H00-private-path-prefixes.txt}"
H00_EMPTY_INVENTORY_SHA256="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
H00_PRODUCT_PATTERN='role[[:punct:][:space:]]*radar|y[[:punct:][:space:]]*t[[:punct:][:space:]]*pipe|llama[[:punct:][:space:]]*swap'

h00_materialize_and_verify_inventory() {
	local path="$1"
	if [[ ! -e "$path" ]]; then
		mkdir -p "$(dirname "$path")"
		(umask 077; : >"$path")
	fi
	[[ -f "$path" ]] || {
		echo "H00 inventory is not a regular file" >&2
		return 1
	}
	chmod 600 "$path"
	[[ "$(stat -c '%a' "$path")" == "600" ]] || {
		echo "H00 inventory mode is not 0600" >&2
		return 1
	}
	local digest count
	digest="$(sha256sum "$path" | cut -d' ' -f1)"
	count="$(grep -cve '^[[:space:]]*$' "$path" || true)"
	[[ "$digest" == "$H00_EMPTY_INVENTORY_SHA256" && "$count" == "0" ]] || {
		echo "H00 static inventory does not match its approved empty input" >&2
		return 1
	}
	return 0
}

h00_verify_static_inventories() {
	h00_materialize_and_verify_inventory "$H00_CONCRETE_INTEGRATION_NAMES_FILE"
	h00_materialize_and_verify_inventory "$H00_PRIVATE_PATH_PREFIXES_FILE"
	H00_CONCRETE_INTEGRATION_NAMES_SHA256="$H00_EMPTY_INVENTORY_SHA256"
	H00_PRIVATE_PATH_PREFIXES_SHA256="$H00_EMPTY_INVENTORY_SHA256"
	H00_CONCRETE_INTEGRATION_NAMES_COUNT=0
	H00_PRIVATE_PATH_PREFIXES_COUNT=0
}

h00_product_scope() {
	local path
	while IFS= read -r path; do
		case "$path" in
		cmd/llm-broker/*|internal/*|migrations/*|openapi.yaml)
			printf '%s\n' "$path"
			;;
		*.go)
			if [[ "$path" != */* ]]; then
				printf '%s\n' "$path"
			fi
			;;
		esac
	done < <(git ls-files)
	return 0
}

h00_all_repo_files() {
	git ls-files
}

h00_repo_absolute_files() {
	local path
	while IFS= read -r path; do
		printf '%s/%s\n' "$PWD" "$path"
	done < <(h00_all_repo_files)
}

h00_payload_files() {
	local root="$1" path
	while IFS= read -r path; do
		case "$path" in
		./scans/product-scan.stdout|scans/product-scan.stdout|\
		./scans/product-scan.stderr|scans/product-scan.stderr|\
		./scans/secret-private-scan.stdout|scans/secret-private-scan.stdout|\
		./scans/secret-private-scan.stderr|scans/secret-private-scan.stderr|\
		./scans/final-scan-attestation.json|scans/final-scan-attestation.json|\
		./MANIFEST.SHA256|MANIFEST.SHA256|./SHA256SUMS|SHA256SUMS)
			continue
			;;
		esac
		printf '%s/%s\n' "$root" "$path"
	done < <(cd "$root" && find . -type f -print | sort)
}

h00_sha256_file() {
	sha256sum "$1" | cut -d' ' -f1
}

h00_sha256_text() {
	printf '%s' "$1" | sha256sum | cut -d' ' -f1
}
