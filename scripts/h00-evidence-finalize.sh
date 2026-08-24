#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"

evidence="${1:?usage: scripts/h00-evidence-finalize.sh EVIDENCE_DIR DYNAMIC_CANARY_FILE}"
canary="${2:?usage: scripts/h00-evidence-finalize.sh EVIDENCE_DIR DYNAMIC_CANARY_FILE}"
[[ -d "$evidence/manifest" && -f "$evidence/manifest/candidate.json" ]] || {
	echo "H00 evidence finalization requires an initialized manifest" >&2
	exit 2
}
[[ -f "$canary" && "$(stat -c '%a' "$canary")" == "600" ]] || {
	echo "H00 evidence finalization requires a mode-0600 dynamic canary file" >&2
	exit 2
}

current_sha="$(git rev-parse HEAD)"
current_tree="$(git rev-parse 'HEAD^{tree}')"
current_branch="$(git branch --show-current)"
[[ "$current_branch" == "h00-c6-correction-candidate" ]] || {
	echo "H00 evidence finalization requires the correction candidate branch" >&2
	exit 1
}
grep -Fq "\"candidate_sha\":\"$current_sha\"" "$evidence/manifest/candidate.json" || {
	echo "H00 evidence candidate SHA does not match HEAD" >&2
	exit 1
}
grep -Fq "\"tree_sha\":\"$current_tree\"" "$evidence/manifest/candidate.json" || {
	echo "H00 evidence candidate tree does not match HEAD" >&2
	exit 1
}
[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || {
	echo "H00 evidence finalization requires a clean candidate worktree" >&2
	exit 1
}
h00_verify_static_inventories

allowed_scans=(
	product-scan.stdout product-scan.stderr
	secret-private-scan.stdout secret-private-scan.stderr
	final-scan-attestation.json
)
mkdir -p "$evidence/scans"
while IFS= read -r path; do
	case "$path" in
	./scans/*)
		name="${path#./scans/}"
		case " ${allowed_scans[*]} " in
		*" $name "*) ;;
		*) echo "unexpected pre-existing scan metadata: $name" >&2; exit 1 ;;
		esac
		;;
	./MANIFEST.SHA256|./SHA256SUMS) echo "integrity files must not pre-exist" >&2; exit 1 ;;
	 esac
done < <(cd "$evidence" && find . -type f -print | sort)

for name in product-scan.stdout product-scan.stderr secret-private-scan.stdout secret-private-scan.stderr; do
	: >"$evidence/scans/$name"
done

scope_file="$(mktemp)"
mapping_file="$(mktemp)"
trap 'rm -f -- "$scope_file" "$mapping_file"' EXIT
h00_product_scope | sort -u >"$scope_file"

{
	while IFS= read -r path; do printf 'product-boundary\trepo/%s\n' "$path"; done <"$scope_file"
	h00_payload_files "$evidence" | sed "s#^$evidence/#secret-private\\tbundle/#"
	h00_repo_absolute_files | sed 's#^#secret-private\trepo/#'
} | LC_ALL=C sort -u >"$mapping_file"

if ! "$root/scripts/check-h00-boundaries.sh" \
	>"$evidence/scans/product-scan.stdout" 2>"$evidence/scans/product-scan.stderr"; then
	exit 1
fi
if ! "$root/scripts/check-h00-artifacts.sh" --payload "$evidence" --canary "$canary" \
	>"$evidence/scans/secret-private-scan.stdout" 2>"$evidence/scans/secret-private-scan.stderr"; then
	exit 1
fi
for name in product-scan.stdout product-scan.stderr secret-private-scan.stdout secret-private-scan.stderr; do
	[[ ! -s "$evidence/scans/$name" ]] || {
		echo "H00 scan output must be empty on success" >&2
		exit 1
	}
done

mapping_sha="$(sha256sum "$mapping_file" | cut -d' ' -f1)"
pattern_sha="$(h00_sha256_text "$H00_PRODUCT_PATTERN")"
scope_sha="$(sha256sum "$scope_file" | cut -d' ' -f1)"
dynamic_sha="$(sha256sum "$canary" | cut -d' ' -f1)"
dynamic_count="$(grep -cve '^[[:space:]]*$' "$canary" || true)"
empty_sha="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
python_version="$(python3 --version 2>&1)"

python3 - "$evidence/scans/final-scan-attestation.json" "$current_sha" "$current_tree" "$mapping_sha" \
	"$pattern_sha" "$scope_sha" "$dynamic_sha" "$dynamic_count" "$python_version" "$empty_sha" <<'PY'
import json
import sys
from pathlib import Path

out, commit, tree, mapping, pattern, scope, dynamic, count, python_version, empty = sys.argv[1:]
zero_scan = {
    "exit_code": 0,
    "stdout_sha256": empty,
    "stdout_bytes": 0,
    "stderr_sha256": empty,
    "stderr_bytes": 0,
}
payload = {
    "candidate_sha": commit,
    "tree_sha": tree,
    "scan_identity_target_file_list_sha256": mapping,
    "product_pattern_set_sha256": pattern,
    "product_scope_sha256": scope,
    "static_inventories": {
        "concrete_integration_names": {"sha256": empty, "entry_count": 0},
        "private_path_prefixes": {"sha256": empty, "entry_count": 0},
    },
    "dynamic_canary": {
        "sha256": dynamic,
        "entry_count": int(count),
        "categories": [
            "role_passwords", "client_tokens", "backend_credentials",
            "credential_urls", "bearer_headers", "temporary_paths",
        ],
    },
    "scans": {
        "product_boundary": {
            "command_identity": "scripts/check-h00-boundaries.sh",
            "command_version": f"candidate:{commit}",
            **zero_scan,
        },
        "secret_private_payload": {
            "command_identity": "scripts/check-h00-artifacts.sh",
            "command_version": f"candidate:{commit};python:{python_version}",
            **zero_scan,
        },
    },
}
Path(out).write_text(json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n")
PY

(cd "$evidence" && sha256sum manifest/candidate.json >MANIFEST.SHA256)
(cd "$evidence" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
(cd "$evidence" && sha256sum -c MANIFEST.SHA256 >/dev/null && sha256sum -c SHA256SUMS >/dev/null)
