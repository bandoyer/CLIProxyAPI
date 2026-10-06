#!/usr/bin/env bash
# Tests for cli-proxy-api-monitor.sh. Runs the script the way the timer does,
# with a fake HOME and stub `curl`, `journalctl` and `notify-send` commands on
# PATH, so the live proxy, the real journal and the desktop are never touched.
#
# Usage: scripts/cli-proxy-api-monitor_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MONITOR_SCRIPT="$SCRIPT_DIR/cli-proxy-api-monitor.sh"

SECRET_KEY='mgmt-TEST-MANAGEMENT-KEY-789'

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

assert_not_contains() {
	local haystack=$1 needle=$2 what=$3
	[[ "$haystack" != *"$needle"* ]] || fail "$what contains [$needle]"
}

# setup builds a fresh fake HOME in which every check passes: the proxy is
# healthy, no credential has a problem, the last backup matches config.yaml
# and the journal is empty. Tests then break one thing. Globals: T (test
# root), HOME_DIR, CONFIG, RECORD.
setup() {
	T="$(mktemp -d)"
	HOME_DIR="$T/home"
	CONFIG="$HOME_DIR/.config/cli-proxy-api/config.yaml"
	RECORD="$HOME_DIR/.local/state/cli-proxy-api/config-backup-last-success"
	mkdir -p "$HOME_DIR/.config/cli-proxy-api" "$(dirname "$RECORD")" "$T/bin"
	printf 'debug: false\n' >"$CONFIG"
	touch -d '-1 hour' "$CONFIG"
	printf 'time=2026-10-06T12:00:00Z\nepoch=1791288000\nsha256=%s\n' \
		"$(sha256sum "$CONFIG" | cut -d' ' -f1)" >"$RECORD"
	(umask 077 && printf '%s\n' "$SECRET_KEY" >"$HOME_DIR/.config/cli-proxy-api/monitor-management.key")
	: >"$T/healthz.status"
	printf '%s\n' '{"observed_at":"2026-10-06T12:00:00Z","files":[]}' >"$T/credentials.json"
	: >"$T/journal.txt"
	: >"$T/notifications"
	: >"$T/curl-args"
	: >"$T/curl-stdin"

	# Stub curl: records its arguments and its stdin (the -K config). The
	# health URL fails when healthz.status holds "fail"; the credentials URL
	# prints credentials.json, or fails when credentials.status holds "fail".
	cat >"$T/bin/curl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_ROOT/curl-args"
url="${*: -1}"
for arg in "$@"; do
	if [[ "$arg" == "-" ]]; then cat >>"$STUB_ROOT/curl-stdin"; fi
done
case "$url" in
*/healthz)
	[[ "$(cat "$STUB_ROOT/healthz.status")" == fail ]] && exit 7
	echo '{"status":"ok"}'
	;;
*/v8/management/credentials)
	[[ "$(cat "$STUB_ROOT/credentials.status" 2>/dev/null)" == fail ]] && exit 22
	cat "$STUB_ROOT/credentials.json"
	;;
*)
	echo "curl stub: unexpected URL $url" >&2
	exit 2
	;;
esac
STUB

	# Stub journalctl: records its arguments and prints journal.txt.
	cat >"$T/bin/journalctl" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_ROOT/journalctl-args"
cat "$STUB_ROOT/journal.txt"
STUB

	# Stub notify-send: one line per notification, arguments joined by " | ".
	cat >"$T/bin/notify-send" <<'STUB'
#!/usr/bin/env bash
(IFS='|' && printf '%s\n' "$*") >>"$STUB_ROOT/notifications"
STUB
	chmod +x "$T/bin/curl" "$T/bin/journalctl" "$T/bin/notify-send"
}

teardown() {
	rm -rf "$T"
}

# run_monitor runs the script as the timer would. Sets STATUS and LOG
# (combined stdout and stderr).
run_monitor() {
	STATUS=0
	LOG="$(env -i \
		HOME="$HOME_DIR" \
		PATH="$T/bin:/usr/bin:/bin" \
		STUB_ROOT="$T" \
		bash "$MONITOR_SCRIPT" 2>&1)" || STATUS=$?
}

notification_count() {
	wc -l <"$T/notifications" | tr -d ' '
}

test_failed_health_check_sends_one_notification() {
	echo fail >"$T/healthz.status"
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "CLIProxyAPI is down" "notification"
	assert_contains "$(cat "$T/notifications")" "critical" "notification urgency"
}

test_healthy_proxy_sends_no_notification() {
	run_monitor
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 0 "$(notification_count)" "notifications"
}

# credentials writes credentials.json with the given file entries, in the
# shape GET /v8/management/credentials returns.
credentials() {
	local IFS=,
	printf '{"observed_at":"2026-10-06T12:00:00Z","files":[%s]}\n' "$*" >"$T/credentials.json"
}

test_credential_with_status_error_sends_one_notification() {
	credentials \
		'{"id":"claude-a.json","name":"claude-a.json","provider":"claude","status":"error","status_message":"unauthorized","disabled":false,"unavailable":true,"cooldowns":[]}' \
		'{"id":"codex-b.json","name":"codex-b.json","provider":"codex","status":"active","status_message":"","disabled":false,"unavailable":false,"cooldowns":[]}'
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "claude-a.json" "notification"
	assert_contains "$(cat "$T/notifications")" "unauthorized" "notification"
}

test_disabled_credential_with_refresh_message_sends_one_notification() {
	credentials \
		'{"id":"claude-a.json","name":"claude-a.json","provider":"claude","status":"disabled","status_message":"disabled (invalid grant)","disabled":true,"unavailable":true,"cooldowns":[]}'
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "claude-a.json" "notification"
}

test_credential_disabled_by_hand_sends_no_notification() {
	credentials \
		'{"id":"claude-a.json","name":"claude-a.json","provider":"claude","status":"disabled","status_message":"disabled via management API","disabled":true,"unavailable":false,"cooldowns":[]}'
	run_monitor
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
}

test_quota_cooldown_sends_no_notification() {
	credentials \
		'{"id":"claude-a.json","name":"claude-a.json","provider":"claude","status":"error","status_message":"quota exhausted","disabled":false,"unavailable":true,"next_retry_after":"2026-10-06T13:00:00Z","cooldowns":[{"scope":"credential","reason":"quota","retry_at":"2026-10-06T13:00:00Z","remaining_seconds":3600}]}' \
		'{"id":"claude-b.json","name":"claude-b.json","provider":"claude","status":"error","status_message":"","disabled":false,"unavailable":true,"cooldowns":[{"scope":"credential","reason":"credential_quota","retry_at":"2026-10-06T13:00:00Z","remaining_seconds":3600}]}'
	run_monitor
	assert_eq 0 "$STATUS" "exit status (log: $LOG)"
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
}

test_management_key_is_sent_on_stdin_and_never_logged() {
	credentials \
		'{"id":"claude-a.json","name":"claude-a.json","provider":"claude","status":"error","status_message":"token expired","disabled":false,"unavailable":true,"cooldowns":[]}'
	run_monitor
	assert_contains "$(cat "$T/curl-stdin")" "Authorization: Bearer $SECRET_KEY" "curl config on stdin"
	assert_not_contains "$(cat "$T/curl-args")" "$SECRET_KEY" "curl arguments"
	assert_not_contains "$LOG" "$SECRET_KEY" "log"
	assert_not_contains "$(cat "$T/notifications")" "$SECRET_KEY" "notifications"
}

test_unreadable_credentials_list_sends_one_notification() {
	echo fail >"$T/credentials.status"
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "credentials" "notification"
}

test_management_key_file_readable_by_others_is_refused() {
	chmod 0644 "$HOME_DIR/.config/cli-proxy-api/monitor-management.key"
	run_monitor
	assert_contains "$LOG" "must be mode 0600" "log"
	assert_not_contains "$(cat "$T/curl-stdin")" "$SECRET_KEY" "curl config on stdin"
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
}

test_transient_upstream_error_sends_no_notification() {
	credentials \
		'{"id":"codex-b.json","name":"codex-b.json","provider":"codex","status":"error","status_message":"transient upstream error","disabled":false,"unavailable":true,"cooldowns":[{"scope":"credential","reason":"transient_upstream","retry_at":"2026-10-06T12:01:00Z","remaining_seconds":60}]}'
	run_monitor
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
}

test_config_changed_since_last_backup_sends_one_notification() {
	printf 'debug: true\n' >"$CONFIG"
	touch -d '-10 minutes' "$CONFIG"
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "not backed up" "notification"
}

test_missing_backup_record_sends_one_notification() {
	rm "$RECORD"
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "not backed up" "notification"
}

test_config_edit_within_grace_period_sends_no_notification() {
	printf 'debug: true\n' >"$CONFIG"
	run_monitor
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
}

test_checks_still_run_when_health_check_fails() {
	echo fail >"$T/healthz.status"
	printf 'debug: true\n' >"$CONFIG"
	touch -d '-10 minutes' "$CONFIG"
	run_monitor
	assert_eq 2 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "CLIProxyAPI is down" "notifications"
	assert_contains "$(cat "$T/notifications")" "not backed up" "notifications"
}

test_ongoing_problems_notify_once_until_they_clear() {
	echo fail >"$T/healthz.status"
	printf 'debug: true\n' >"$CONFIG"
	touch -d '-10 minutes' "$CONFIG"
	run_monitor
	run_monitor
	assert_eq 2 "$(notification_count)" "notifications after two runs (log: $LOG)"

	# The proxy comes back with a broken credential, and the backup is still stale.
	: >"$T/healthz.status"
	credentials \
		'{"id":"claude-a.json","name":"claude-a.json","provider":"claude","status":"error","status_message":"unauthorized","disabled":false,"unavailable":true,"cooldowns":[]}'
	run_monitor
	run_monitor
	assert_eq 3 "$(notification_count)" "notifications after recovery (log: $LOG)"

	# The proxy goes down again: a new outage notifies again.
	echo fail >"$T/healthz.status"
	run_monitor
	assert_eq 4 "$(notification_count)" "notifications after the second outage (log: $LOG)"
	assert_eq 2 "$(grep -c 'CLIProxyAPI is down' "$T/notifications")" "outage notifications"
}

# Reset times relative to now, inside the monitor's 24-hour journal window.
RESET_1="$(date -u -d '-3 hours' +%Y-%m-%dT%H:%M:%SZ)"
RESET_2="$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)"
# Older than the alert state keeps (twice the journal window).
RESET_OLD="$(date -u -d '-3 days' +%Y-%m-%dT%H:%M:%SZ)"

# reset_line prints a window-reset log line as `journalctl -o cat` shows it.
# Arguments: credential, window, share_left, reset_at, kind.
reset_line() {
	printf '[2026-10-06 14:00:41] [--------] [info ] [reset_log.go:90] quota window reset | credential=%s provider=claude window=%s share_left=%s reset_at=%s kind=%s learned_at=2026-10-06T12:00:00Z\n' "$@"
}

test_longest_window_reset_with_quota_left_sends_one_notification() {
	reset_line 'claude dlb.json' seven_day 0.4200 "$RESET_1" ranking >"$T/journal.txt"
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications (log: $LOG)"
	assert_contains "$(cat "$T/notifications")" "claude dlb.json" "notification"
	assert_contains "$(cat "$T/notifications")" "seven_day" "notification"
	assert_contains "$(cat "$T/notifications")" "42%" "notification"
	assert_contains "$(cat "$T/journalctl-args")" "--user -u cli-proxy-api.service" "journalctl arguments"
}

test_wasted_quota_alert_fires_once_per_credential_per_reset() {
	reset_line claude-a.json seven_day 0.4200 "$RESET_1" ranking >"$T/journal.txt"
	run_monitor
	run_monitor
	assert_eq 1 "$(notification_count)" "notifications after two runs (log: $LOG)"
	reset_line claude-a.json seven_day 0.3000 "$RESET_2" ranking >>"$T/journal.txt"
	reset_line claude-b.json seven_day 0.3000 "$RESET_1" ranking >>"$T/journal.txt"
	run_monitor
	assert_eq 3 "$(notification_count)" "notifications after the next resets (log: $LOG)"
}

test_reset_older_than_alert_state_sends_no_notification() {
	reset_line claude-a.json seven_day 0.4200 "$RESET_OLD" ranking >"$T/journal.txt"
	run_monitor
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
}

test_reset_with_twenty_percent_or_less_left_sends_no_notification() {
	reset_line claude-a.json seven_day 0.2000 "$RESET_1" ranking >"$T/journal.txt"
	reset_line claude-b.json seven_day 0.0000 "$RESET_1" ranking >>"$T/journal.txt"
	run_monitor
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
}

test_shorter_window_reset_sends_no_notification() {
	reset_line claude-a.json five_hour 0.9000 "$RESET_1" gating >"$T/journal.txt"
	reset_line claude-a.json seven_day_opus 0.9000 "$RESET_1" per-model >>"$T/journal.txt"
	run_monitor
	assert_eq 0 "$(notification_count)" "notifications (log: $LOG)"
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
