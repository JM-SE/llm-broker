#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"

output="${1:-/home/jmse/homelab/ai/projects/llm-broker/generated/H00/$(git rev-parse HEAD)}"
[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || {
	echo "H00 evidence candidate must be created from a clean worktree" >&2
	exit 1
}
mkdir -p "$output"/{manifest,remote-ci,automated,manual,recovery,scans}
h00_verify_static_inventories
commit_sha="$(git rev-parse HEAD)"
tree_sha="$(git rev-parse 'HEAD^{tree}')"
utc="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
scope_file="$(mktemp)"
trap 'rm -f -- "$scope_file"' EXIT
h00_product_scope | sort -u >"$scope_file"
scope_sha="$(sha256sum "$scope_file" | cut -d' ' -f1)"
pattern_sha="$(h00_sha256_text "$H00_PRODUCT_PATTERN")"

printf '{"candidate_sha":"%s","tree_sha":"%s","created_utc":"%s","concrete_inventory_sha256":"%s","concrete_inventory_count":0,"private_path_inventory_sha256":"%s","private_path_inventory_count":0,"product_pattern_sha256":"%s","product_scope_sha256":"%s"}\n' \
	"$commit_sha" "$tree_sha" "$utc" "$H00_CONCRETE_INTEGRATION_NAMES_SHA256" \
	"$H00_PRIVATE_PATH_PREFIXES_SHA256" "$pattern_sha" "$scope_sha" >"$output/manifest/candidate.json"

{
	date -u '+%Y-%m-%dT%H:%M:%SZ'
	go version
	docker version --format '{{.Server.Version}}' 2>/dev/null || true
	docker compose version 2>/dev/null || true
} >"$output/manifest/tool-versions.txt"

cat >"$output/manifest/commands.txt" <<'EOF'
date -u +%Y-%m-%dT%H:%M:%SZ
git rev-parse HEAD
git rev-parse HEAD^{tree}
git status --porcelain=v1 --untracked-files=all
make check
EOF
printf 'role probes: pending execution\n' >"$output/manifest/role-map.txt"
printf 'product_pattern_sha256=%s\n' "$pattern_sha" >"$output/manifest/scan-patterns.txt"
printf 'product_scope_sha256=%s\n' "$scope_sha" >"$output/manifest/scope-manifest.txt"
