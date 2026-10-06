#!/usr/bin/env bash
# Tests for cli-proxy-api-routing-report.sh. Runs the report with a stub
# `journalctl` on PATH that prints a fixture journal, so the real journal is
# never read.
#
# Usage: scripts/cli-proxy-api-routing-report_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPORT_SCRIPT="$SCRIPT_DIR/cli-proxy-api-routing-report.sh"

failures=0
current_test=''

fail() {
	printf 'FAIL %s: %s\n' "$current_test" "$*" >&2
	failures=$((failures + 1))
}

assert_eq() {
	local want=$1 got=$2 what=$3
	[[ "$got" == "$want" ]] || fail "$what: want [$want], got [$got]"
}

assert_contains() {
	local haystack=$1 needle=$2 what=$3
	[[ "$haystack" == *"$needle"* ]] || fail "$what lacks [$needle]: [$haystack]"
}

# assert_row checks that a report line, with runs of spaces squeezed to one,
# equals the given row.
assert_row() {
	local row=$1
	grep -qxF -- "$row" <<<"$(tr -s ' ' <<<"$OUT" | sed 's/^ //; s/ $//')" ||
		fail "report lacks row [$row]: [$OUT]"
}

setup() {
	T="$(mktemp -d)"
	mkdir -p "$T/bin"
	: >"$T/journal.txt"
	# Stub journalctl: records its arguments and prints journal.txt.
	cat >"$T/bin/journalctl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_ROOT/journalctl-args"
cat "$STUB_ROOT/journal.txt"
STUB
	chmod +x "$T/bin/journalctl"
}

teardown() {
	rm -rf "$T"
}

# run_report runs the report with the given arguments. Sets STATUS and OUT
# (stdout) and ERR (stderr).
run_report() {
	STATUS=0
	OUT="$(env -i HOME="$T" PATH="$T/bin:/usr/bin:/bin" STUB_ROOT="$T" \
		bash "$REPORT_SCRIPT" "$@" 2>"$T/stderr")" || STATUS=$?
	ERR="$(cat "$T/stderr")"
}

# journal appends lines to the fixture journal, each with the prefix that
# the proxy's log formatter adds.
journal() {
	local line
	for line in "$@"; do
		printf '[2026-10-06 14:00:41] [a1b2c3d4] [info ] [file.go:1] %s\n' "$line" >>"$T/journal.txt"
	done
}

test_report_reads_the_chosen_number_of_hours_from_the_service_journal() {
	run_report 6
	assert_eq 0 "$STATUS" "exit status (stderr: $ERR)"
	assert_contains "$(cat "$T/journalctl-args")" "--user -u cli-proxy-api.service --since -6h" "journalctl arguments"
	assert_contains "$OUT" "last 6 hours" "report"
}

test_report_prints_wasted_quota_per_credential_and_window() {
	journal \
		'quota window reset | credential=claude dlb.json provider=claude window=seven_day share_left=0.4200 reset_at=2026-10-06T14:00:00Z kind=ranking learned_at=2026-10-06T12:00:00Z' \
		'quota window reset | credential=claude dlb.json provider=claude window=five_hour share_left=0.1000 reset_at=2026-10-06T09:00:00Z kind=gating learned_at=2026-10-06T08:00:00Z' \
		'quota window reset | credential=claude dlb.json provider=claude window=five_hour share_left=0.3000 reset_at=2026-10-06T14:00:00Z kind=gating learned_at=2026-10-06T13:00:00Z' \
		'quota window reset | credential=codex-b.json provider=codex window=weekly share_left=0.0000 reset_at=2026-10-06T10:00:00Z kind=ranking learned_at=2026-10-06T09:00:00Z'
	run_report 24
	assert_eq 0 "$STATUS" "exit status (stderr: $ERR)"
	assert_contains "$OUT" "Wasted quota per credential and window" "report"
	assert_row "CREDENTIAL WINDOW KIND RESETS WASTED LAST"
	assert_row "claude dlb.json seven_day ranking 1 42% 42%"
	assert_row "claude dlb.json five_hour gating 2 40% 30%"
	assert_row "codex-b.json weekly ranking 1 0% 0%"
}

test_report_prints_picks_per_credential_by_reason() {
	journal \
		'expiring-first pick | credential=claude dlb.json urgency=13.33%/h reason=more_urgent thread=header:thread-1 provider=mixed model=claude-sonnet-4-5' \
		'expiring-first pick | credential=claude dlb.json urgency=13.10%/h reason=binding_kept thread=header:thread-1 provider=mixed model=claude-sonnet-4-5' \
		'expiring-first pick | credential=claude dlb.json urgency=13.00%/h reason=binding_kept thread=header:thread-1 provider=mixed model=claude-sonnet-4-5' \
		'expiring-first pick | credential=codex-b.json urgency=no_data reason=no_data thread=- provider=mixed model=gpt-5' \
		'expiring-first pick | credential=codex-b.json urgency=no_data reason=credit_only thread=codex:abc provider=mixed model=gpt-5'
	run_report 24
	assert_eq 0 "$STATUS" "exit status (stderr: $ERR)"
	assert_contains "$OUT" "Picks per credential" "report"
	assert_row "CREDENTIAL PICKS MORE_URGENT NO_DATA BINDING_KEPT CREDIT_ONLY"
	assert_row "claude dlb.json 3 1 0 2 0"
	assert_row "codex-b.json 2 0 1 0 1"
}

# journal_as appends one line with a given request ID ("--------" for none),
# as the proxy's log formatter writes it.
journal_as() {
	printf '[2026-10-06 14:00:41] [%s] [info ] [file.go:1] %s\n' "$1" "$2" >>"$T/journal.txt"
}

test_report_prints_binding_moves_per_thread_with_reasons() {
	# Thread 1 is bound to claude-a. A reading shows its window exhausted: the
	# next request re-picks claude b and then logs the ended binding, both
	# with that request's ID.
	journal_as r1 'expiring-first pick | credential=claude-a.json urgency=10.00%/h reason=more_urgent thread=header:thread 1 provider=mixed model=claude-sonnet-4-5'
	journal_as r2 'expiring-first pick | credential=claude-a.json urgency=10.00%/h reason=binding_kept thread=header:thread 1 provider=mixed model=claude-sonnet-4-5'
	journal_as r3 'expiring-first pick | credential=claude b.json urgency=8.00%/h reason=more_urgent thread=header:thread 1 provider=mixed model=claude-sonnet-4-5'
	journal_as r3 'affinity binding ended | thread=header:thread 1 credential=claude-a.json provider=claude model=claude-sonnet-4-5 reason=quota_window_exhausted'
	# Thread 2 gets a 429 on claude b: the ended binding is logged with no
	# request ID, and the next request picks claude-a.
	journal_as r4 'expiring-first pick | credential=claude b.json urgency=8.00%/h reason=more_urgent thread=claude:uuid-2 provider=mixed model=claude-opus-4-5'
	journal_as -------- 'affinity binding ended | thread=claude:uuid-2 credential=claude b.json provider=claude model=claude-opus-4-5 reason=quota_exceeded_429'
	journal_as r5 'expiring-first pick | credential=claude-a.json urgency=9.00%/h reason=more_urgent thread=claude:uuid-2 provider=mixed model=claude-opus-4-5'
	# Thread 1 again: claude b is disabled, and no request follows yet.
	journal_as -------- 'affinity binding ended | thread=header:thread 1 credential=claude b.json provider=claude model=claude-sonnet-4-5 reason=disabled'
	# Thread 3 re-picks after its binding expired: not a move.
	journal_as r6 'expiring-first pick | credential=codex-c.json urgency=no_data reason=no_data thread=codex:uuid-3 provider=mixed model=gpt-5'
	run_report 24
	assert_eq 0 "$STATUS" "exit status (stderr: $ERR)"
	assert_contains "$OUT" "Binding moves per thread" "report"
	assert_row "THREAD MOVE FROM TO REASON"
	assert_row "header:thread 1 1 claude-a.json claude b.json quota_window_exhausted"
	assert_row "header:thread 1 2 claude b.json ? disabled"
	assert_row "claude:uuid-2 1 claude b.json claude-a.json quota_exceeded_429"
	[[ "$OUT" != *codex:uuid-3* ]] || fail "re-pick after expiry reported as a move: [$OUT]"
}

test_report_prints_none_for_each_summary_without_data() {
	run_report 24
	assert_eq 0 "$STATUS" "exit status (stderr: $ERR)"
	assert_eq 3 "$(grep -c '^  none$' <<<"$OUT")" "summaries without data (report: $OUT)"
}

test_report_rejects_a_bad_number_of_hours() {
	run_report 6h
	assert_eq 2 "$STATUS" "exit status"
	assert_contains "$ERR" "usage" "stderr"
	[[ ! -e "$T/journalctl-args" ]] || fail "journalctl was called"
}

main() {
	local t
	for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
		current_test=$t
		setup
		"$t"
		teardown
	done
	if ((failures > 0)); then
		printf '%d failure(s)\n' "$failures" >&2
		exit 1
	fi
	echo PASS
}

main
