#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck source=scripts/h00-scan-common.sh
source "$root/scripts/h00-scan-common.sh"
# shellcheck source=scripts/h00-evidence-capture-common.sh
source "$root/scripts/h00-evidence-capture-common.sh"

evidence=""
canary=""
parsed_canary=""
dry_run=0
while [[ $# -gt 0 ]]; do
	case "$1" in
		--evidence) evidence="${2:?missing evidence directory}"; shift 2 ;;
		--canary) canary="${2:?missing dynamic canary file}"; shift 2 ;;
		--parsed-canary) parsed_canary="${2:?missing parsed canary file}"; shift 2 ;;
		--dry-run) dry_run=1; shift ;;
		*) echo "usage: scripts/h00-evidence-preflight.sh --evidence DIR --canary FILE [--parsed-canary FILE] [--dry-run]" >&2; exit 2 ;;
	esac
done
[[ -d "$evidence" && -f "$evidence/manifest/candidate.json" && -f "$canary" ]] || {
	echo "H00 preflight requires an evidence directory, candidate manifest, and dynamic canary" >&2
	exit 2
}

fail() { echo "H00 preflight failed: $1" >&2; exit 1; }

current_sha="$(git rev-parse HEAD)"
current_tree="$(git rev-parse 'HEAD^{tree}')"
python3 - "$evidence/manifest/candidate.json" "$current_sha" "$current_tree" <<'PY' || fail "candidate identity mismatch"
import json, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("candidate_sha") != sys.argv[2] or data.get("tree_sha") != sys.argv[3]:
    raise SystemExit(1)
for key in ("candidate_sha", "tree_sha", "created_utc", "product_pattern_sha256", "product_scope_sha256"):
    if not data.get(key):
        raise SystemExit(1)
PY
if [[ "$dry_run" == 0 ]]; then
	[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || fail "checkout is dirty"
else
	echo "H00 preflight identity cleanliness check bypassed for dry-run only" >&2
fi

required=(
	manifest/candidate.json manifest/commands.txt manifest/role-map.txt
	manifest/scan-patterns.txt manifest/scope-manifest.txt
	manifest/static-inventory-verification.txt manifest/command-descriptors.tsv
	manifest/scanner-descriptors.tsv manifest/e3-mapping-rules.tsv
	candidate-evidence/README.txt candidate-evidence/E1-candidate-identity.txt
	candidate-evidence/E2-local-gate.txt candidate-evidence/E3-focused-log-map.tsv
	candidate-evidence/E4-bundle-provenance.txt candidate-evidence/E5-compose-manual-checklist.txt
	candidate-evidence/E6-recovery-cleanup-checklist.txt candidate-evidence/E7-no-live-contact.txt
	automated/role-probe.log automated/full-race.log automated/make-check.log
	automated/h00migration.log automated/postgres-focused.log automated/api-focused.log
	automated/worker-focused.log automated/cmd-focused.log automated/config-policy-focused.log
	manual/compose-complete.log manual/pre-apply-policy-proof.tsv
	recovery/restore.log recovery/cleanup.log recovery/filesystem-cleanup.tsv
	recovery/inventory-before-final.txt recovery/inventory-after-final.txt recovery/pg-test-up.log
)
for path in "${required[@]}"; do
	[[ -f "$evidence/$path" ]] || fail "missing required artifact: $path"
done
while IFS= read -r link; do
	fail "symlink in evidence payload: ${link#"$evidence"/}"
done < <(find "$evidence" -type l -print)
while IFS= read -r path; do
	if grep -IEnq 'pending|placeholder|stale|TODO|DYNAMIC_CANARY|POINTER_FILE|(^|[^[:alnum:]])BUNDLE([^[:alnum:]]|$)' "$path"; then
		fail "unfinished marker in ${path#"$evidence"/}"
	fi
	grep -IEnq '^result=FAIL$|^exit_code=[1-9]' "$path" && fail "contradictory failure result in ${path#"$evidence"/}" || true
done < <(find "$evidence" -type f ! -path '*/scans/*' -print)

expected_roles="$(mktemp)"
map_keys="$(mktemp)"
expected_keys="$(mktemp)"
compose_json="$(mktemp)"
parsed_owned=0
if [[ -z "$parsed_canary" ]]; then
	parsed_canary="$(mktemp)"
	parsed_owned=1
fi
categories_file="$(mktemp)"
cleanup() {
	rm -f -- "$expected_roles" "$map_keys" "$expected_keys" "$compose_json" "$categories_file"
	if [[ "$parsed_owned" == 1 ]]; then rm -f -- "$parsed_canary"; fi
}
trap cleanup EXIT
h00_validate_dynamic_canary "$canary" "$parsed_canary" "$categories_file" || fail "dynamic canary schema"

grep -E '^(migration_owner|provisioner|runtime)_(purpose|current_user):' "$evidence/automated/role-probe.log" >"$expected_roles"
[[ -s "$expected_roles" ]] || fail "role probe is incomplete"
cmp -s "$expected_roles" "$evidence/manifest/role-map.txt" || fail "role map disagrees with role probe"

for log in "$evidence"/automated/*.log; do
	[[ "${log##*/}" == "role-probe.log" ]] && continue
	for key in h00_log_version descriptor_id command start_utc end_utc exit_code packages test_subtest_count; do
		grep -Eq "^${key}=.+$" "$log" || fail "missing ${key} metadata in ${log#"$evidence"/}"
	done
	grep -Eq '^h00_log_version=1$' "$log" || fail "unsupported log envelope in ${log#"$evidence"/}"
	grep -Eq '^exit_code=0$' "$log" || fail "focused command failed: ${log#"$evidence"/}"
	grep -Eq '^start_utc=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' "$log" || fail "invalid start timestamp"
	grep -Eq '^end_utc=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' "$log" || fail "invalid end timestamp"
	start="$(awk -F= '$1=="start_utc"{print $2; exit}' "$log")"
	end="$(awk -F= '$1=="end_utc"{print $2; exit}' "$log")"
	[[ "$start" < "$end" || "$start" == "$end" ]] || fail "log time order is invalid"
	actual_count="$(grep -c '^test_subtest=' "$log" || true)"
	declared_count="$(awk -F= '$1=="test_subtest_count"{print $2; exit}' "$log")"
	[[ "$actual_count" == "$declared_count" ]] || fail "test/subtest metadata count mismatch"
	descriptor="$(awk -F= '$1=="descriptor_id"{print $2; exit}' "$log")"
	relative_log="${log#"$evidence"/}"
	awk -F'\t' -v d="$descriptor" -v o="$relative_log" '$1 == d && $3 == o && $5 == 0 {found=1} END {exit !found}' "$evidence/manifest/commands.txt" || fail "command ledger does not describe $relative_log"
	done

while IFS=$'\t' read -r descriptor command output packages exit_code; do
	[[ -n "$descriptor" ]] || continue
	[[ "$exit_code" == 0 && -n "$command" && -n "$output" && -n "$packages" ]] || fail "malformed command ledger row"
	[[ "$output" != /* && -f "$evidence/$output" ]] || fail "command ledger output path is missing or private"
	case "$command" in
		*BUNDLE*|*POINTER_FILE*|*DYNAMIC_CANARY*|*PASSWORD*|*TOKEN*) fail "unsafe command ledger descriptor" ;;
	esac
	case "$command" in
		*postgres://*) fail "unsafe command ledger descriptor" ;;
	esac
	case "$command" in
		*://*@*) fail "unsafe command ledger descriptor" ;;
	esac
done <"$evidence/manifest/commands.txt"
while IFS=$'\t' read -r descriptor command output packages exit_code; do
	[[ "$descriptor" == descriptor_id ]] && continue
	[[ -n "$descriptor" && -n "$command" && -n "$output" && -n "$packages" ]] || fail "malformed command descriptor"
done <"$evidence/manifest/command-descriptors.tsv"
grep -Fqx $'product_boundary\tscripts/check-h00-boundaries.sh' "$evidence/manifest/scanner-descriptors.tsv" || fail "product scanner identity missing"
grep -Fqx $'secret_private_payload\tscripts/check-h00-artifacts.sh' "$evidence/manifest/scanner-descriptors.tsv" || fail "payload scanner identity missing"

map="$evidence/candidate-evidence/E3-focused-log-map.tsv"
[[ "$(head -n 1 "$map")" == $'log\ttest_subtest\tids\trationale\tresult' ]] || fail "E3 map header is invalid"
while IFS=$'\t' read -r log test ids rationale result; do
	[[ -n "$log" && -n "$test" && -n "$ids" && -n "$rationale" && "$result" == PASS ]] || fail "malformed E3 row"
	[[ -f "$evidence/$log" ]] || fail "E3 references missing log"
	grep -Fqx "test_subtest=$test" "$evidence/$log" || fail "E3 references a test absent from its log"
	if [[ "$ids" != NONE_NON_H00 ]]; then
		for id in ${ids//,/ }; do [[ "$id" =~ ^((R([1-9]|10)|C[1-6])(\.[0-9]+)*)$ ]] || fail "invalid E3 acceptance ID"; done
	else
		[[ -n "$rationale" ]] || fail "NONE_NON_H00 row lacks rationale"
	fi
	printf '%s\t%s\n' "$log" "$test"
done < <(tail -n +2 "$map") >"$map_keys"
sort -o "$map_keys" "$map_keys"
if [[ -n "$(uniq -d "$map_keys")" ]]; then fail "duplicate E3 row"; fi
for log in "$evidence"/automated/*.log; do
	[[ "${log##*/}" == "role-probe.log" ]] && continue
	while IFS= read -r test; do
		printf '%s\t%s\n' "${log#"$evidence"/}" "$test"
	done < <(sed -n 's/^test_subtest=//p' "$log")
done >"$expected_keys"
sort -o "$expected_keys" "$expected_keys"
cmp -s "$map_keys" "$expected_keys" || fail "E3 map does not exactly cover emitted tests/subtests"

required_preapply=(zero_capability_children behavioral_deny_all topology_digest_unprovisioned timeout_default_absent timeout_old_check_absent timeout_range_check active_task_index queued_running_predicate)
for property in "${required_preapply[@]}"; do
	awk -F'\t' -v property="$property" '$1 == property && $4 == "PASS" && NF >= 5 {found=1} END {exit !found}' "$evidence/manual/pre-apply-policy-proof.tsv" || fail "missing pre-apply proof: $property"
done
required_cleanup=(compose_env db_env pointer client_credential probe_source probe_binary restore_dump restore_database)
for category in "${required_cleanup[@]}"; do
	awk -F'\t' -v category="$category" '$1 == category && ($2 == "absent=true" || $2 == "removed=true") {found=1} END {exit !found}' "$evidence/recovery/filesystem-cleanup.tsv" || fail "missing cleanup proof: $category"
done

grep -Eq '^effective_ports=none$' "$evidence/manual/compose-complete.log" || fail "effective Compose ports are not proven absent"
grep -Eq '^h00_evidence_internal=true$' "$evidence/manual/compose-complete.log" || fail "internal network is not proven"
grep -Eq '^attachments=db,migrate,policy-apply,role-bootstrap,api,worker$' "$evidence/manual/compose-complete.log" || fail "Compose attachments are incomplete"
grep -Eq '^blocked_egress_destination=[^[:space:]]+$' "$evidence/manual/compose-complete.log" || fail "blocked egress destination is missing"
grep -Eq '^blocked_egress_class=[^[:space:]]+$' "$evidence/manual/compose-complete.log" || fail "blocked egress classification is missing"
for result in health ready task replay conflict; do
	grep -Eq "^${result}=PASS$" "$evidence/manual/compose-complete.log" || fail "E7 ${result} result missing"
done

if ! env \
	LLM_BROKER_DB_PASSWORD=phase_a_dummy_db \
	LLM_BROKER_PROVISIONER_PASSWORD=phase_a_dummy_provisioner \
	LLM_BROKER_RUNTIME_PASSWORD=phase_a_dummy_runtime \
	H00_EVIDENCE_NETWORK_NAME=h00-preflight-effective-network \
	docker compose -f compose.example.yaml -f compose.evidence-internal.yaml config --format json >"$compose_json" 2>/dev/null; then
	fail "effective Compose configuration could not be rendered"
fi
python3 - "$compose_json" <<'PY' || fail "effective Compose isolation contract"
import json, sys
cfg = json.load(open(sys.argv[1], encoding="utf-8"))
if cfg["services"].get("db", {}).get("ports") or cfg["services"].get("api", {}).get("ports"):
    raise SystemExit(1)
if cfg["networks"].get("h00_evidence", {}).get("internal") is not True:
    raise SystemExit(1)
for name in ("db", "migrate", "policy-apply", "role-bootstrap", "api", "worker"):
    if "h00_evidence" not in cfg["services"].get(name, {}).get("networks", {}):
        raise SystemExit(1)
PY

h00_verify_static_inventories || fail "static inventory verification"
for line in \
	"concrete_integration_names_mode=600" \
	"concrete_integration_names_sha256=$H00_EMPTY_INVENTORY_SHA256" \
	"concrete_integration_names_count=0" \
	"private_path_prefixes_mode=600" \
	"private_path_prefixes_sha256=$H00_EMPTY_INVENTORY_SHA256" \
	"private_path_prefixes_count=0"; do
	grep -Fqx "$line" "$evidence/manifest/static-inventory-verification.txt" || fail "static inventory evidence mismatch"
done
[[ -z "$(find "$evidence/scans" -type f -print -quit)" ]] || fail "E8 outputs exist before preflight"
if [[ "$dry_run" == 1 ]]; then echo "H00 preflight PASS (dry-run)"; else echo "H00 preflight PASS"; fi
