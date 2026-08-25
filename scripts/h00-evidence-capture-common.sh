#!/usr/bin/env bash

# Focused, single-command evidence capture primitives. Callers provide one
# sanitized command descriptor per invocation; this file is not a rehearsal
# orchestrator.

set -euo pipefail

h00_capture_command() {
	local descriptor="$1" output="$2" packages="$3" sanitized_command="$4"
	shift 4
	[[ "${1:-}" == "--" ]] || { echo "capture command requires -- before executable" >&2; return 2; }
	shift
	[[ -n "$descriptor" && -n "$output" && -n "$packages" && -n "$sanitized_command" ]] || {
		echo "capture command requires descriptor, output, packages, and sanitized command" >&2
		return 2
	}
	case "$sanitized_command" in
		*BUNDLE*|*POINTER_FILE*|*DYNAMIC_CANARY*|*PASSWORD*|*TOKEN*|*postgres://*|*://*@*)
			echo "capture command descriptor contains a forbidden placeholder or secret" >&2
			return 2
		;;
	esac
	local tmp start end status output_path
	local -a names
	tmp="$(mktemp)"
	output_path="$output"
	if [[ -n "${H00_EVIDENCE_DIR:-}" && "$output" != /* ]]; then
		output_path="$H00_EVIDENCE_DIR/$output"
	fi
	start="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	set +e
	"$@" >"$tmp" 2>&1
	status=$?
	set -e
	end="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	{
		printf 'h00_log_version=1\n'
		printf 'descriptor_id=%s\n' "$descriptor"
		printf 'command=%s\n' "$sanitized_command"
		printf 'start_utc=%s\n' "$start"
		printf 'end_utc=%s\n' "$end"
		printf 'exit_code=%s\n' "$status"
		printf 'packages=%s\n' "$packages"
		mapfile -t names < <(sed -n 's/^=== RUN[[:space:]]*//p' "$tmp")
		printf 'test_subtest_count=%s\n' "${#names[@]}"
		local name
		for name in "${names[@]}"; do
			printf 'test_subtest=%s\n' "$name"
		done
		printf '%s\n' '--- output ---'
		cat "$tmp"
	} >"$output_path"
	if [[ -n "${H00_EVIDENCE_COMMAND_LEDGER:-}" ]]; then
		printf '%s\t%s\t%s\t%s\t%s\n' "$descriptor" "$sanitized_command" "$output" "$packages" "$status" \
			>>"$H00_EVIDENCE_COMMAND_LEDGER"
	fi
	if [[ -n "${H00_EVIDENCE_COMMAND_DESCRIPTOR_FILE:-}" ]]; then
		printf '%s\t%s\t%s\t%s\t%s\n' "$descriptor" "$sanitized_command" "$output" "$packages" "$status" \
			>>"$H00_EVIDENCE_COMMAND_DESCRIPTOR_FILE"
	fi
	rm -f -- "$tmp"
	return "$status"
}

h00_generate_role_map() {
	local probe="$1" output="$2"
	[[ -f "$probe" ]] || { echo "missing role probe" >&2; return 1; }
	for role in migration_owner provisioner runtime; do
		grep -Eq "^${role}_(purpose|current_user):.+$" "$probe" || {
			echo "role probe is incomplete" >&2
			return 1
		}
	done
	grep -E '^(migration_owner|provisioner|runtime)_(purpose|current_user):' "$probe" >"$output"
}

h00_generate_e3_map() {
	local evidence="$1" output="$2"
	local rules="$evidence/manifest/e3-mapping-rules.tsv"
	[[ -f "$rules" ]] || { echo "missing E3 mapping rules" >&2; return 1; }
	printf 'log\ttest_subtest\tids\trationale\tresult\n' >"$output"
	while IFS=$'\t' read -r log_name test_name; do
		[[ -n "$log_name" && -n "$test_name" ]] || continue
		local descriptor ids rationale
		descriptor="$(awk -F= '$1 == "descriptor_id" {print $2; exit}' "$evidence/$log_name")"
		IFS=$'\t' read -r _ ids rationale < <(awk -F$'\t' -v d="$descriptor" '$1 == d {print; exit}' "$rules")
		[[ -n "$ids" && -n "$rationale" ]] || {
			echo "missing E3 rule for $descriptor" >&2
			return 1
		}
		printf '%s\t%s\t%s\t%s\tPASS\n' "$log_name" "$test_name" "$ids" "$rationale" >>"$output"
	done < <(
		for log in "$evidence"/automated/*.log; do
			[[ -f "$log" ]] || continue
			while IFS= read -r test_name; do
				printf '%s\t%s\n' "${log#"$evidence"/}" "$test_name"
			done < <(sed -n 's/^test_subtest=//p' "$log")
		done
	)
}

h00_validate_dynamic_canary() {
	local source_file="$1" values_file="$2" categories_file="$3"
	[[ -f "$source_file" && ! -L "$source_file" && "$(stat -c '%a' "$source_file")" == "600" ]] || {
		echo "H00 dynamic canary source must be a mode-0600 regular non-symlink" >&2
		return 1
	}
	: >"$values_file"
	: >"$categories_file"
	local line category value tab_count
	local -A seen_category=() seen_pair=()
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ -n "$line" ]] || continue
		tab_count="${line//[^$'\t']/}"
		[[ "$tab_count" == $'\t' ]] || { echo "H00 dynamic canary record is malformed" >&2; return 1; }
		category="${line%%$'\t'*}"
		value="${line#*$'\t'}"
		case "$category" in
			role_passwords|client_tokens|backend_credentials|credential_urls|bearer_headers|temporary_paths) ;;
			*) echo "H00 dynamic canary category is not approved" >&2; return 1 ;;
		esac
		[[ -n "$value" && "$value" != *$'\t'* ]] || { echo "H00 dynamic canary value is empty or contains a tab" >&2; return 1; }
		pair="$category$'\t'$value"
		[[ -z "${seen_pair[$pair]:-}" ]] || { echo "H00 dynamic canary contains a duplicate" >&2; return 1; }
		seen_pair["$pair"]=1
		seen_category["$category"]=1
		printf '%s\n' "$value" >>"$values_file"
	done <"$source_file"
	for category in role_passwords client_tokens backend_credentials credential_urls bearer_headers temporary_paths; do
		[[ -n "${seen_category[$category]:-}" ]] || {
			echo "H00 dynamic canary category is missing" >&2
			return 1
		}
		printf '%s\n' "$category" >>"$categories_file"
	done
	chmod 600 "$values_file" "$categories_file"
}
