#!/usr/bin/env bash
# Tests for config-backup.sh. Runs the script the way the systemd service
# does, with a fake HOME and stub `op` and `sleep` commands on PATH, so no
# real 1Password call and no wall-clock wait happens.
#
# Usage: scripts/config-backup.test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_SCRIPT="$SCRIPT_DIR/config-backup.sh"

SECRET_TOKEN='ops_TEST-SERVICE-ACCOUNT-TOKEN-xyz'
CONFIG_V1='remote-management:
  secret-key: "mgmt-SECRET-123"
api-keys:
  - "client-SECRET-456"'
CONFIG_V1_SHA256='d56bcabb3b45b0d66940ad4a88e5cddd81656baf12938c349eeedc0aef656bcc'
CONFIG_V2='debug: true'
CONFIG_V2_SHA256='f867fe538171ee003592210869c10c6cec11e4e479b1c90c78738c1678e5786d'

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

assert_not_contains() {
	local haystack=$1 needle=$2 what=$3
	[[ "$haystack" != *"$needle"* ]] || fail "$what contains [$needle]"
}

assert_contains() {
	local haystack=$1 needle=$2 what=$3
	[[ "$haystack" == *"$needle"* ]] || fail "$what lacks [$needle]: [$haystack]"
}

# setup builds a fresh fake HOME with a config.yaml, a 0600 token file and
# stub commands. Globals: T (test root), HOME_DIR, CONFIG, RECORD.
setup() {
	T="$(mktemp -d)"
	HOME_DIR="$T/home"
	CONFIG="$HOME_DIR/.config/cli-proxy-api/config.yaml"
	RECORD="$HOME_DIR/.local/state/cli-proxy-api/config-backup-last-success"
	mkdir -p "$HOME_DIR/.config/cli-proxy-api" "$T/bin" "$T/uploads" "$T/edits"
	printf '%s\n' "$CONFIG_V1" >"$CONFIG"
	(umask 077 && printf '%s\n' "$SECRET_TOKEN" >"$HOME_DIR/.config/cli-proxy-api/config-backup.token")
	: >"$T/op-calls"
	# The vault already holds the config.yaml document unless a test says
	# otherwise.
	printf '%s\n' '[{"id":"abc123","title":"config.yaml","category":"DOCUMENT"}]' >"$T/items.json"

	# Stub op: `item list` prints items.json. Each `document` call is recorded
	# in op-calls with its uploaded file content and the service account token
	# it received through the environment. With OP_STUB_FAIL=1 every call
	# fails with a message that echoes the token.
	cat >"$T/bin/op" <<'STUB'
#!/usr/bin/env bash
if [[ "${OP_STUB_FAIL:-0}" == 1 ]]; then
	echo "[ERROR] could not edit document: unauthorized token ${OP_SERVICE_ACCOUNT_TOKEN:-}" >&2
	exit 1
fi
case "$1 $2" in
"item list")
	[[ "${OP_STUB_WARN:-0}" == 1 ]] && echo "[WARN] a newer version of op is available" >&2
	cat "$STUB_ROOT/items.json"
	;;
"document edit" | "document create")
	n=$(($(wc -l <"$STUB_ROOT/op-calls") + 1))
	printf '%s\n' "$*" >>"$STUB_ROOT/op-calls"
	printf '%s' "${OP_SERVICE_ACCOUNT_TOKEN:-}" >"$STUB_ROOT/op-token-$n"
	if [[ "$2" == edit ]]; then cp "$4" "$STUB_ROOT/uploads/$n"; else cp "$3" "$STUB_ROOT/uploads/$n"; fi
	if [[ -f "$STUB_ROOT/edit-during-upload" ]]; then
		mv -f "$STUB_ROOT/edit-during-upload" "$STUB_CONFIG"
	fi
	;;
*)
	echo "op stub: unexpected call: $*" >&2
	exit 2
	;;
esac
STUB

	# Stub sleep: each call applies the next queued edit (edits/1, edits/2, ...)
	# to config.yaml, simulating the panel writing during the debounce window.
	cat >"$T/bin/sleep" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_ROOT/sleep-calls"
next=$(find "$STUB_ROOT/edits" -type f | sort -n | head -n 1)
if [[ -n "$next" ]]; then
	cat "$next" >"$STUB_CONFIG"
	rm -f "$next"
fi
STUB
	chmod +x "$T/bin/op" "$T/bin/sleep"
}

teardown() {
	rm -rf "$T"
}

# queue_edit appends a config edit that the next stub sleep applies.
queue_edit() {
	local n
	n=$(($(find "$T/edits" -type f | wc -l) + 1))
	printf '%s\n' "$1" >"$T/edits/$n"
}

# run_backup runs the script as the service would. Sets STATUS and LOG
# (combined stdout and stderr).
run_backup() {
	STATUS=0
	LOG="$(env -i \
		HOME="$HOME_DIR" \
		PATH="$T/bin:/usr/bin:/bin" \
		STUB_ROOT="$T" \
		STUB_CONFIG="$CONFIG" \
		"$@" \
		bash "$BACKUP_SCRIPT" 2>&1)" || STATUS=$?
}

op_call_count() {
	wc -l <"$T/op-calls" | tr -d ' '
}

test_config_edit_updates_1password_document_once_after_debounce() {
	run_backup
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 1 "$(op_call_count)" "op calls"
	local call
	call="$(head -n 1 "$T/op-calls")"
	assert_eq "document edit config.yaml" "$(cut -d' ' -f1-3 <<<"$call")" "op command"
	assert_contains "$call" "--vault CLIProxyAPI" "op arguments"
	assert_contains "$call" "--file-name config.yaml" "op arguments"
	assert_eq "$CONFIG_V1" "$(cat "$T/uploads/1" 2>/dev/null)" "uploaded content"
	assert_eq "$SECRET_TOKEN" "$(cat "$T/op-token-1" 2>/dev/null)" "token passed to op"
	assert_eq 10 "$(head -n 1 "$T/sleep-calls" 2>/dev/null)" "debounce seconds"
	assert_contains "$(cat "$RECORD" 2>/dev/null)" "sha256=$CONFIG_V1_SHA256" "last-success record"
}

test_first_backup_creates_the_1password_document() {
	printf '%s\n' '[]' >"$T/items.json"
	run_backup
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 1 "$(op_call_count)" "op calls"
	local call
	call="$(head -n 1 "$T/op-calls")"
	assert_eq "document create" "$(cut -d' ' -f1-2 <<<"$call")" "op command"
	assert_contains "$call" "--title config.yaml" "op arguments"
	assert_contains "$call" "--vault CLIProxyAPI" "op arguments"
	assert_contains "$call" "--file-name config.yaml" "op arguments"
	assert_eq "$CONFIG_V1" "$(cat "$T/uploads/1" 2>/dev/null)" "uploaded content"
	assert_contains "$(cat "$RECORD" 2>/dev/null)" "sha256=$CONFIG_V1_SHA256" "last-success record"
}

test_op_warning_does_not_create_a_duplicate_document() {
	run_backup OP_STUB_WARN=1
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq "document edit" "$(head -n 1 "$T/op-calls" | cut -d' ' -f1-2)" "op command"
}

test_unreadable_item_list_fails_without_creating_a_document() {
	printf '%s\n' 'not json' >"$T/items.json"
	run_backup
	[[ "$STATUS" != 0 ]] || fail "exit status: want non-zero, got 0"
	assert_eq 0 "$(op_call_count)" "op calls"
	assert_contains "$LOG" "backup failed" "log"
}

test_several_quick_edits_produce_one_update_with_the_final_content() {
	queue_edit 'debug: false'
	queue_edit "$CONFIG_V2"
	run_backup
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 1 "$(op_call_count)" "op calls"
	assert_eq "$CONFIG_V2" "$(cat "$T/uploads/1" 2>/dev/null)" "uploaded content"
	assert_eq 3 "$(wc -l <"$T/sleep-calls" | tr -d ' ')" "debounce waits"
	assert_contains "$(cat "$RECORD" 2>/dev/null)" "sha256=$CONFIG_V2_SHA256" "last-success record"
}

test_failed_update_leaves_last_success_record_unchanged_and_logs_error() {
	local previous="time=2026-10-01T00:00:00Z
epoch=1790812800
sha256=$CONFIG_V2_SHA256"
	mkdir -p "$(dirname "$RECORD")"
	printf '%s\n' "$previous" >"$RECORD"
	run_backup OP_STUB_FAIL=1
	[[ "$STATUS" != 0 ]] || fail "exit status: want non-zero, got 0"
	assert_eq "$previous" "$(cat "$RECORD")" "last-success record"
	assert_contains "$LOG" "could not edit document" "log"
	assert_contains "$LOG" "backup failed" "log"
}

test_no_secret_is_written_to_logs() {
	run_backup OP_STUB_FAIL=1
	assert_not_contains "$LOG" "$SECRET_TOKEN" "failure log"
	assert_not_contains "$LOG" "mgmt-SECRET-123" "failure log"
	assert_not_contains "$LOG" "client-SECRET-456" "failure log"
	run_backup
	assert_not_contains "$LOG" "$SECRET_TOKEN" "success log"
	assert_not_contains "$LOG" "mgmt-SECRET-123" "success log"
	assert_not_contains "$LOG" "client-SECRET-456" "success log"
}

test_rewrite_with_unchanged_content_makes_no_update() {
	local previous="time=2026-10-01T00:00:00Z
epoch=1790812800
sha256=$CONFIG_V1_SHA256"
	mkdir -p "$(dirname "$RECORD")"
	printf '%s\n' "$previous" >"$RECORD"
	run_backup
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 0 "$(op_call_count)" "op calls"
	assert_eq "$previous" "$(cat "$RECORD")" "last-success record"
}

test_edit_during_upload_is_backed_up_by_the_same_run() {
	printf '%s\n' "$CONFIG_V2" >"$T/edit-during-upload"
	run_backup
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 2 "$(op_call_count)" "op calls"
	assert_eq "$CONFIG_V1" "$(cat "$T/uploads/1" 2>/dev/null)" "first upload"
	assert_eq "$CONFIG_V2" "$(cat "$T/uploads/2" 2>/dev/null)" "second upload"
	assert_contains "$(cat "$RECORD" 2>/dev/null)" "sha256=$CONFIG_V2_SHA256" "last-success record"
}

test_token_file_readable_by_others_is_refused() {
	chmod 0644 "$HOME_DIR/.config/cli-proxy-api/config-backup.token"
	run_backup
	[[ "$STATUS" != 0 ]] || fail "exit status: want non-zero, got 0"
	assert_eq 0 "$(op_call_count)" "op calls"
	assert_contains "$LOG" "must be mode 0600" "log"
	assert_not_contains "$LOG" "$SECRET_TOKEN" "log"
	[[ ! -e "$RECORD" ]] || fail "last-success record was written"
}

test_missing_token_file_is_reported() {
	rm "$HOME_DIR/.config/cli-proxy-api/config-backup.token"
	run_backup
	[[ "$STATUS" != 0 ]] || fail "exit status: want non-zero, got 0"
	assert_eq 0 "$(op_call_count)" "op calls"
	assert_contains "$LOG" "token file" "log"
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
