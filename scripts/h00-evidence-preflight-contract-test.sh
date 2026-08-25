#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
preflight="$root/scripts/h00-evidence-preflight.sh"
fixture="$(mktemp -d)"
canary="$(mktemp)"
trap 'rm -rf -- "$fixture"; rm -f -- "$canary"' EXIT

make_fixture() {
	local out="$1"
	mkdir -p "$out"/{manifest,candidate-evidence,automated,manual,recovery,scans}
	local sha tree
	sha="$(git rev-parse HEAD)"
	tree="$(git rev-parse 'HEAD^{tree}')"
	python3 - "$out/manifest/candidate.json" "$sha" "$tree" <<'PY'
import json, sys
Path = sys.argv[1]
json.dump({
    "candidate_sha": sys.argv[2], "tree_sha": sys.argv[3],
    "created_utc": "2026-08-25T01:00:00Z",
    "product_pattern_sha256": "fixture-pattern",
    "product_scope_sha256": "fixture-scope",
}, open(Path, "w", encoding="utf-8"), separators=(",", ":"))
PY
	: >"$out/manifest/commands.txt"
	printf 'descriptor_id\tsanitized_command\toutput\tpackages\texit_code\n' >"$out/manifest/command-descriptors.tsv"
	printf 'scanner_id\tcommand_identity\nproduct_boundary\tscripts/check-h00-boundaries.sh\nsecret_private_payload\tscripts/check-h00-artifacts.sh\n' >"$out/manifest/scanner-descriptors.tsv"
	printf 'descriptor_id\tids\trationale\n' >"$out/manifest/e3-mapping-rules.tsv"
	printf 'fixture\n' >"$out/manifest/scan-patterns.txt"
	printf 'fixture\n' >"$out/manifest/scope-manifest.txt"
	printf 'concrete_integration_names_mode=600\nconcrete_integration_names_sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\nconcrete_integration_names_count=0\nprivate_path_prefixes_mode=600\nprivate_path_prefixes_sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\nprivate_path_prefixes_count=0\n' >"$out/manifest/static-inventory-verification.txt"
	printf '%s\n' \
		'migration_owner_purpose: schema migration' \
		'migration_owner_current_user: postgres' \
		'provisioner_purpose: policy administration' \
		'provisioner_current_user: llm_broker_provisioner' \
		'runtime_purpose: runtime execution' \
		'runtime_current_user: llm_broker_runtime' >"$out/automated/role-probe.log"
	cp "$out/automated/role-probe.log" "$out/manifest/role-map.txt"
	local spec descriptor name
	while IFS=$'\t' read -r spec descriptor name; do
		printf 'h00_log_version=1\ndescriptor_id=%s\ncommand=fixture command %s\nstart_utc=2026-08-25T01:00:00Z\nend_utc=2026-08-25T01:00:01Z\nexit_code=0\npackages=%s\ntest_subtest_count=1\ntest_subtest=%s\n--- output ---\n=== RUN   %s\n--- PASS: %s (0.00s)\n' \
			"$descriptor" "$descriptor" "$spec" "$name" "$name" "$name" >"$out/$spec"
		printf '%s\tfixture command %s\t%s\t%s\t0\n' "$descriptor" "$descriptor" "$spec" "$spec" >>"$out/manifest/commands.txt"
		printf '%s\tfixture command %s\t%s\t%s\t0\n' "$descriptor" "$descriptor" "$spec" "$spec" >>"$out/manifest/command-descriptors.tsv"
		printf '%s\tR1\tfixture coverage\n' "$descriptor" >>"$out/manifest/e3-mapping-rules.tsv"
	done <<'EOF'
automated/full-race.log	full-race	TestFullRace
automated/make-check.log	make-check	TestMakeCheck
automated/h00migration.log	h00migration	TestMigration
automated/postgres-focused.log	postgres-focused	TestPostgres
automated/api-focused.log	api-focused	TestAPI
automated/worker-focused.log	worker-focused	TestWorker
automated/cmd-focused.log	cmd-focused	TestCommand
automated/config-policy-focused.log	config-policy-focused	TestConfigPolicy
EOF
	{
		printf 'log\ttest_subtest\tids\trationale\tresult\n'
		for spec in "$out"/automated/*.log; do
			[[ "${spec##*/}" == role-probe.log ]] && continue
			printf '%s\t%s\tR1\tfixture coverage\tPASS\n' "${spec#"$out"/}" "$(sed -n 's/^test_subtest=//p' "$spec")"
		done
	} >"$out/candidate-evidence/E3-focused-log-map.tsv"
	for file in README.txt E1-candidate-identity.txt E2-local-gate.txt E4-bundle-provenance.txt E5-compose-manual-checklist.txt E6-recovery-cleanup-checklist.txt E7-no-live-contact.txt; do
		printf 'result=PASS\n' >"$out/candidate-evidence/$file"
	done
	cat >"$out/manual/pre-apply-policy-proof.tsv" <<'EOF'
property	expected	observed	result	source
zero_capability_children	zero	zero	PASS	fixture
behavioral_deny_all	deny-all	deny-all	PASS	fixture
topology_digest_unprovisioned	null	null	PASS	fixture
timeout_default_absent	absent	absent	PASS	fixture
timeout_old_check_absent	absent	absent	PASS	fixture
timeout_range_check	1..3600	1..3600	PASS	fixture
active_task_index	(client_id,workload_id)	(client_id,workload_id)	PASS	fixture
queued_running_predicate	queued_or_running	queued_or_running	PASS	fixture
EOF
	cat >"$out/recovery/filesystem-cleanup.tsv" <<'EOF'
category	result
compose_env	absent=true
db_env	absent=true
pointer	absent=true
client_credential	absent=true
probe_source	absent=true
probe_binary	absent=true
restore_dump	absent=true
restore_database	removed=true
EOF
	printf 'effective_ports=none\nh00_evidence_internal=true\nattachments=db,migrate,policy-apply,role-bootstrap,api,worker\nblocked_egress_destination=example.com\nblocked_egress_class=internal-network-denied\nhealth=PASS\nready=PASS\ntask=PASS\nreplay=PASS\nconflict=PASS\n' >"$out/manual/compose-complete.log"
	printf 'restore=PASS\n' >"$out/recovery/restore.log"
	printf 'cleanup=PASS\n' >"$out/recovery/cleanup.log"
	printf 'before=PASS\n' >"$out/recovery/inventory-before-final.txt"
	printf 'after=PASS\n' >"$out/recovery/inventory-after-final.txt"
	printf 'test environment pointer written\n' >"$out/recovery/pg-test-up.log"
}

make_fixture "$fixture"
chmod 600 "$canary"
cat >"$canary" <<'EOF'
role_passwords	fixture-role-password
client_tokens	fixture-client-token
backend_credentials	fixture-backend-credential
credential_urls	fixture-credential-url
bearer_headers	fixture-bearer-header
temporary_paths	/tmp/fixture-temporary-path
EOF

"$preflight" --evidence "$fixture" --canary "$canary" --dry-run >/dev/null

expect_fail() {
	if "$@" >/dev/null 2>&1; then
		echo "H00 preflight fixture unexpectedly passed: $*" >&2
		exit 1
	fi
}

mutate_and_fail() {
	local label="$1" target="$2" replacement="$3"
	local bad
	bad="$(mktemp -d)"
	cp -R "$fixture/." "$bad/"
	python3 - "$bad/$target" "$replacement" <<'PY'
from pathlib import Path
import sys
path, replacement = sys.argv[1:]
text = Path(path).read_text()
if "\n" in replacement:
    text = text + replacement
else:
    text = text.replace(text.splitlines()[0], replacement, 1)
Path(path).write_text(text)
PY
	expect_fail "$preflight" --evidence "$bad" --canary "$canary" --dry-run
	rm -rf -- "$bad"
	printf '%s=PASS\n' "$label"
}

mutate_and_fail stale-role-map manifest/role-map.txt 'role probes: pending execution'
mutate_and_fail inherited-ports manual/compose-complete.log 'effective_ports=published'
mutate_and_fail forbidden-marker candidate-evidence/README.txt 'placeholder'

bad_network="$(mktemp -d)"
cp -R "$fixture/." "$bad_network/"
python3 - "$bad_network/manual/compose-complete.log" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
path.write_text(path.read_text().replace("h00_evidence_internal=true", "h00_evidence_internal=false"))
PY
expect_fail "$preflight" --evidence "$bad_network" --canary "$canary" --dry-run
rm -rf -- "$bad_network"

bad_attachments="$(mktemp -d)"
cp -R "$fixture/." "$bad_attachments/"
python3 - "$bad_attachments/manual/compose-complete.log" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
path.write_text(path.read_text().replace("attachments=db,migrate,policy-apply,role-bootstrap,api,worker", "attachments=db,api"))
PY
expect_fail "$preflight" --evidence "$bad_attachments" --canary "$canary" --dry-run
rm -rf -- "$bad_attachments"

bad_scanner="$(mktemp -d)"
cp -R "$fixture/." "$bad_scanner/"
python3 - "$bad_scanner/manifest/scanner-descriptors.tsv" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
path.write_text(path.read_text().replace("scripts/check-h00-boundaries.sh", "scripts/other-scanner.sh"))
PY
expect_fail "$preflight" --evidence "$bad_scanner" --canary "$canary" --dry-run
rm -rf -- "$bad_scanner"

bad_extra_map="$(mktemp -d)"
cp -R "$fixture/." "$bad_extra_map/"
printf 'automated/api-focused.log\tNotEmitted\tR1\textra row\tPASS\n' >>"$bad_extra_map/candidate-evidence/E3-focused-log-map.tsv"
expect_fail "$preflight" --evidence "$bad_extra_map" --canary "$canary" --dry-run
rm -rf -- "$bad_extra_map"

bad_commands="$(mktemp -d)"
cp -R "$fixture/." "$bad_commands/"
printf 'missing\tfixture command\tmissing.log\tfixture\t0\n' >>"$bad_commands/manifest/commands.txt"
expect_fail "$preflight" --evidence "$bad_commands" --canary "$canary" --dry-run
rm -rf -- "$bad_commands"

bad_metadata="$(mktemp -d)"
cp -R "$fixture/." "$bad_metadata/"
python3 - "$bad_metadata/automated/api-focused.log" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
path.write_text("\n".join(line for line in path.read_text().splitlines() if not line.startswith("start_utc=")) + "\n")
PY
expect_fail "$preflight" --evidence "$bad_metadata" --canary "$canary" --dry-run
rm -rf -- "$bad_metadata"

bad_preapply="$(mktemp -d)"
cp -R "$fixture/." "$bad_preapply/"
python3 - "$bad_preapply/manual/pre-apply-policy-proof.tsv" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
path.write_text("\n".join(line for line in path.read_text().splitlines() if not line.startswith("behavioral_deny_all\t")) + "\n")
PY
expect_fail "$preflight" --evidence "$bad_preapply" --canary "$canary" --dry-run
rm -rf -- "$bad_preapply"

bad_cleanup="$(mktemp -d)"
cp -R "$fixture/." "$bad_cleanup/"
python3 - "$bad_cleanup/recovery/filesystem-cleanup.tsv" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
path.write_text("\n".join(line for line in path.read_text().splitlines() if not line.startswith("pointer\t")) + "\n")
PY
expect_fail "$preflight" --evidence "$bad_cleanup" --canary "$canary" --dry-run
rm -rf -- "$bad_cleanup"

bad_map="$(mktemp -d)"
cp -R "$fixture/." "$bad_map/"
python3 - "$bad_map/candidate-evidence/E3-focused-log-map.tsv" <<'PY'
from pathlib import Path
path = Path(__import__("sys").argv[1])
lines = path.read_text().splitlines()
path.write_text("\n".join(lines[:-1]) + "\n")
PY
expect_fail "$preflight" --evidence "$bad_map" --canary "$canary" --dry-run
rm -rf -- "$bad_map"

bad_canary="$(mktemp)"
chmod 600 "$bad_canary"
printf 'role_passwords\tmissing-categories\n' >"$bad_canary"
expect_fail "$preflight" --evidence "$fixture" --canary "$bad_canary" --dry-run
rm -f -- "$bad_canary"

bad_duplicate_canary="$(mktemp)"
chmod 600 "$bad_duplicate_canary"
printf '%s\n' $'role_passwords\tduplicate-one' $'role_passwords\tduplicate-one' $'client_tokens\tone' $'backend_credentials\tone' $'credential_urls\tone' $'bearer_headers\tone' $'temporary_paths\t/tmp/duplicate' >"$bad_duplicate_canary"
expect_fail "$preflight" --evidence "$fixture" --canary "$bad_duplicate_canary" --dry-run
rm -f -- "$bad_duplicate_canary"

bad_static_dir="$(mktemp -d)"
concrete_inventory="$bad_static_dir/concrete"
private_inventory="$bad_static_dir/private"
printf '' >"$concrete_inventory"
chmod 600 "$concrete_inventory"
ln -s "$concrete_inventory" "$private_inventory"
expect_fail env H00_CONCRETE_INTEGRATION_NAMES_FILE="$concrete_inventory" H00_PRIVATE_PATH_PREFIXES_FILE="$private_inventory" "$preflight" --evidence "$fixture" --canary "$canary" --dry-run
rm -rf -- "$bad_static_dir"

bad_identity="$(mktemp -d)"
cp -R "$fixture/." "$bad_identity/"
python3 - "$bad_identity/manifest/candidate.json" <<'PY'
import json
from pathlib import Path
import sys
path = Path(sys.argv[1])
data = json.loads(path.read_text(encoding="utf-8"))
data["candidate_sha"] = "0" * 40
path.write_text(json.dumps(data, separators=(",", ":")), encoding="utf-8")
PY
expect_fail "$preflight" --evidence "$bad_identity" --canary "$canary"
rm -rf -- "$bad_identity"
echo "H00 preflight contract fixtures PASS"
