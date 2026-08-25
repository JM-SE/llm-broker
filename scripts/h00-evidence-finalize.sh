#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"
# shellcheck source=scripts/h00-evidence-capture-common.sh
source "$root/scripts/h00-evidence-capture-common.sh"

evidence="${1:?usage: scripts/h00-evidence-finalize.sh EVIDENCE_DIR DYNAMIC_CANARY_FILE}"
canary="${2:?usage: scripts/h00-evidence-finalize.sh EVIDENCE_DIR DYNAMIC_CANARY_FILE}"
[[ -d "$evidence/manifest" && -f "$evidence/manifest/candidate.json" ]] || {
	echo "H00 evidence finalization requires an initialized manifest" >&2
	exit 2
}

parsed_canary="$(mktemp)"
categories_file="$(mktemp)"
cleanup_canary() { rm -f -- "$parsed_canary" "$categories_file" "$canary"; }
trap cleanup_canary EXIT HUP INT TERM
h00_validate_dynamic_canary "$canary" "$parsed_canary" "$categories_file"

"$root/scripts/h00-evidence-preflight.sh" --evidence "$evidence" --canary "$canary" --parsed-canary "$parsed_canary"

allowed_scans=(product-scan.stdout product-scan.stderr secret-private-scan.stdout secret-private-scan.stderr final-scan-attestation.json)
while IFS= read -r path; do
	case "$path" in
		./scans/*)
			name="${path#./scans/}"
			case " ${allowed_scans[*]} " in *" $name "*) ;; *) echo "unexpected pre-existing scan metadata: $name" >&2; exit 1 ;; esac
			;;
		./MANIFEST.SHA256|./SHA256SUMS) echo "integrity files must not pre-exist" >&2; exit 1 ;;
		*) ;;
	esac
done < <(cd "$evidence" && find . -type f -print | sort)

for name in product-scan.stdout product-scan.stderr secret-private-scan.stdout secret-private-scan.stderr; do
	: >"$evidence/scans/$name"
done

if ! "$root/scripts/check-h00-boundaries.sh" >"$evidence/scans/product-scan.stdout" 2>"$evidence/scans/product-scan.stderr"; then
	exit 1
fi
if ! "$root/scripts/check-h00-artifacts.sh" --payload "$evidence" --canary "$canary" --parsed-canary "$parsed_canary" \
	>"$evidence/scans/secret-private-scan.stdout" 2>"$evidence/scans/secret-private-scan.stderr"; then
	exit 1
fi
for name in product-scan.stdout product-scan.stderr secret-private-scan.stdout secret-private-scan.stderr; do
	[[ ! -s "$evidence/scans/$name" ]] || { echo "H00 scan output must be empty on success" >&2; exit 1; }
done

scope_file="$(mktemp)"
mapping_file="$(mktemp)"
trap 'rm -f -- "$scope_file" "$mapping_file" "$parsed_canary" "$categories_file" "$canary"' EXIT HUP INT TERM
h00_product_scope | sort -u >"$scope_file"
{
	while IFS= read -r path; do printf 'product-boundary\trepo/%s\n' "$path"; done <"$scope_file"
	h00_payload_files "$evidence" | sed "s#^$evidence/#secret-private\tbundle/#"
	h00_repo_absolute_files | sed 's#^#secret-private\trepo/#'
} | LC_ALL=C sort -u >"$mapping_file"

mapping_sha="$(sha256sum "$mapping_file" | cut -d' ' -f1)"
pattern_sha="$(h00_sha256_text "$H00_PRODUCT_PATTERN")"
scope_sha="$(sha256sum "$scope_file" | cut -d' ' -f1)"
dynamic_sha="$(sha256sum "$canary" | cut -d' ' -f1)"
dynamic_count="$(grep -cve '^[[:space:]]*$' "$canary" || true)"
categories="$(paste -sd, "$categories_file")"
empty_sha="$H00_EMPTY_INVENTORY_SHA256"
python_version="$(python3 --version 2>&1)"
current_sha="$(git rev-parse HEAD)"
current_tree="$(git rev-parse 'HEAD^{tree}')"

python3 - "$evidence/scans/final-scan-attestation.json" "$current_sha" "$current_tree" "$mapping_sha" \
	"$pattern_sha" "$scope_sha" "$dynamic_sha" "$dynamic_count" "$categories" "$python_version" "$empty_sha" <<'PY'
import json
import sys
from pathlib import Path

out, commit, tree, mapping, pattern, scope, dynamic, count, categories, python_version, empty = sys.argv[1:]
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
        "categories": categories.split(","),
    },
    "scans": {
        "product_boundary": {
            "command_identity": "scripts/check-h00-boundaries.sh",
            "command_version": f"candidate:{commit}",
            **zero_scan,
        },
        "secret_private_payload": {
            "command_identity": "scripts/check-h00-artifacts.sh --payload --canary --parsed-canary",
            "command_version": f"candidate:{commit};python:{python_version}",
            **zero_scan,
        },
    },
}
Path(out).write_text(json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n")
PY

(cd "$evidence" && sha256sum manifest/candidate.json >MANIFEST.SHA256)
(cd "$evidence" && find . -type f ! -name SHA256SUMS -printf '%P\0' | LC_ALL=C sort -z | xargs -0 sha256sum >SHA256SUMS)
python3 - "$evidence" <<'PY'
import hashlib
import sys
from pathlib import Path

root = Path(sys.argv[1])
expected = sorted(str(path.relative_to(root)) for path in root.rglob("*") if path.is_file() and path.name != "SHA256SUMS")
actual = []
for line in (root / "SHA256SUMS").read_text().splitlines():
    digest, path = line.split("  ", 1)
    if len(digest) != 64:
        raise SystemExit("invalid checksum digest")
    actual.append(path)
if actual != expected or len(actual) != len(set(actual)):
    raise SystemExit("checksum file set does not cover the payload exactly once")
PY
(cd "$evidence" && sha256sum -c MANIFEST.SHA256 >/dev/null && sha256sum -c SHA256SUMS >/dev/null)
