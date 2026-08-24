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

# The canonical manifest and captured payload must be frozen before either
# scanner runs. The scanners intentionally produce no successful output.

mkdir -p "$evidence/scans"
: >"$evidence/scans/product-scan.stdout"
: >"$evidence/scans/product-scan.stderr"
: >"$evidence/scans/secret-private-scan.stdout"
: >"$evidence/scans/secret-private-scan.stderr"

scope_file="$(mktemp)"
trap 'rm -f -- "$scope_file"' EXIT
h00_product_scope | sort -u >"$scope_file"

printf 'payload_scope_sha256=%s\n' "$(sha256sum <(h00_payload_files "$evidence") | cut -d' ' -f1)" >"$evidence/manifest/payload-scope.txt"

if ! "$root/scripts/check-h00-boundaries.sh" --manifest "$evidence/manifest/product-scan.txt" \
	>"$evidence/scans/product-scan.stdout" 2>"$evidence/scans/product-scan.stderr"; then
	exit 1
fi
if ! "$root/scripts/check-h00-artifacts.sh" --payload "$evidence" --canary "$canary" \
	--manifest "$evidence/manifest/secret-private-scan.txt" \
	>"$evidence/scans/secret-private-scan.stdout" 2>"$evidence/scans/secret-private-scan.stderr"; then
	exit 1
fi

printf 'product_scan=PASS\nsecret_private_scan=PASS\n' >"$evidence/scans/scan-status.txt"
printf '{"schema":1,"product_scan":"PASS","secret_private_scan":"PASS","payload_scope_sha256":"%s"}\n' \
	"$(sha256sum <(h00_payload_files "$evidence") | cut -d' ' -f1)" >"$evidence/manifest/final-scan-attestation.json"

(cd "$evidence" && sha256sum manifest/candidate.json >MANIFEST.SHA256)
(cd "$evidence" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
(cd "$evidence" && sha256sum -c MANIFEST.SHA256 >/dev/null && sha256sum -c SHA256SUMS >/dev/null)
