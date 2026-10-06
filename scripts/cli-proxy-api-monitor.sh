#!/usr/bin/env bash
# Check CLIProxyAPI and send a desktop notification for each problem found.
#
# Run every 5 minutes by cli-proxy-api-monitor.timer. Install steps are in
# scripts/README.md. The checks:
#   - GET /healthz fails (critical);
#   - a credential in GET /v8/management/credentials has status error, or is
#     disabled with a refresh message (unauthorized, invalid grant, token
#     expired); quota cooldowns and transient upstream errors are skipped;
#   - config.yaml differs from the last successful backup (see
#     cli-proxy-api-config-backup.sh), after a grace period;
#   - the window-reset log in the journal shows a longest window
#     (kind=ranking) that reset with more than 20% left, once per credential,
#     window and reset time.
# An ongoing problem notifies once, and again only after it clears and
# comes back.
#
# Settings (environment, all optional; ~/.config and ~/.local/state follow
# XDG_CONFIG_HOME and XDG_STATE_HOME when set):
#   CLI_PROXY_API_HEALTH_URL           (default http://127.0.0.1:8317/healthz)
#   CLI_PROXY_API_CREDENTIALS_URL      (default http://127.0.0.1:8317/v8/management/credentials)
#   CLI_PROXY_API_MANAGEMENT_KEY_FILE  management key, mode 0600
#                                      (default ~/.config/cli-proxy-api/monitor-management.key)
#   CLI_PROXY_API_UNIT                 journald unit (default cli-proxy-api.service)
#   CONFIG_BACKUP_FILE, CONFIG_BACKUP_RECORD
#                                      as in cli-proxy-api-config-backup.sh
#   MONITOR_BACKUP_GRACE_SECONDS       config edit age before a stale backup
#                                      notifies (default 300)
#   MONITOR_JOURNAL_HOURS              journal hours read for resets (default 24)
#   MONITOR_WASTED_QUOTA_THRESHOLD     share left that counts as wasted
#                                      (default 0.2, strictly more alerts)
#   MONITOR_STATE_DIR                  alert state
#                                      (default ~/.local/state/cli-proxy-api/monitor)
#
# The management key reaches curl as a config file on stdin, never as a
# process argument, and is never logged.
set -euo pipefail

config_home="${XDG_CONFIG_HOME:-$HOME/.config}"
state_home="${XDG_STATE_HOME:-$HOME/.local/state}"
health_url="${CLI_PROXY_API_HEALTH_URL:-http://127.0.0.1:8317/healthz}"
credentials_url="${CLI_PROXY_API_CREDENTIALS_URL:-http://127.0.0.1:8317/v8/management/credentials}"
key_file="${CLI_PROXY_API_MANAGEMENT_KEY_FILE:-$config_home/cli-proxy-api/monitor-management.key}"
# The backup settings use the same variables and defaults as
# cli-proxy-api-config-backup.sh.
config_file="${CONFIG_BACKUP_FILE:-$config_home/cli-proxy-api/config.yaml}"
backup_record="${CONFIG_BACKUP_RECORD:-$state_home/cli-proxy-api/config-backup-last-success}"
backup_grace="${MONITOR_BACKUP_GRACE_SECONDS:-300}"
unit="${CLI_PROXY_API_UNIT:-cli-proxy-api.service}"
journal_hours="${MONITOR_JOURNAL_HOURS:-24}"
wasted_threshold="${MONITOR_WASTED_QUOTA_THRESHOLD:-0.2}"
state_dir="${MONITOR_STATE_DIR:-$state_home/cli-proxy-api/monitor}"

log() {
	printf 'monitor: %s\n' "$*" >&2
}

notify() {
	local urgency=$1 title=$2 body=$3
	log "$title: $body"
	notify-send -u "$urgency" -a cli-proxy-api "$title" "$body" || log "notify-send failed"
}

# problem records an ongoing problem under a key and notifies only when the
# key was not a problem in the previous run, so the 5-minute timer sends one
# notification per problem until it clears. Globals: previous_problems and
# current_problems (files with one key per line).
problem() {
	local key=$1
	shift
	printf '%s\n' "$key" >>"$current_problems"
	grep -qxF -- "$key" "$previous_problems" || notify "$@"
}

# keep_problems carries over the previous run's problems whose keys start
# with a prefix, for a check that could not run this time.
keep_problems() {
	local prefix=$1
	awk -v prefix="$prefix" 'index($0, prefix) == 1' "$previous_problems" >>"$current_problems"
}

check_health() {
	if ! curl -fsS --max-time 10 "$health_url" >/dev/null 2>&1; then
		problem down critical "CLIProxyAPI is down" "GET $health_url failed. See: journalctl --user -u cli-proxy-api.service"
		return 1
	fi
}

# read_key prints the management key after checking that only the owner can
# read the key file.
read_key() {
	local mode
	if [[ ! -f "$key_file" ]]; then
		log "management key file $key_file not found"
		return 1
	fi
	mode="$(stat -c %a "$key_file")"
	if (((8#$mode & 8#077) != 0)); then
		log "management key file $key_file must be mode 0600 (is $mode)"
		return 1
	fi
	printf '%s' "$(<"$key_file")"
}

# fetch_credentials prints the credential list. The key goes to curl as a
# config file on stdin, so it never appears in the process arguments.
fetch_credentials() {
	local key
	key="$(read_key)" || return 1
	printf 'header = "Authorization: Bearer %s"\n' "$key" |
		curl -fsS --max-time 10 -K - "$credentials_url" 2>/dev/null
}

# credential_problems prints one "name<TAB>status<TAB>message" line per
# credential that needs a re-login: status error, or disabled with a refresh
# message. Quota cooldowns and transient upstream errors clear by themselves,
# so they are skipped.
credential_problems() {
	jq -r '
		def quota_cooldown:
			.status_message == "quota exhausted"
			or any(.cooldowns[]?; .reason == "quota" or .reason == "credential_quota");
		def refresh_message:
			(.status_message // "") | test("unauthorized|invalid[ _]grant|token expired"; "i");
		.files[]
		| select(
			(.status == "error" and (quota_cooldown | not) and .status_message != "transient upstream error")
			or ((.disabled == true or .status == "disabled") and refresh_message))
		| [.name // .id, .status, (.status_message // "")] | @tsv'
}

check_credentials() {
	local body name status message
	if ! body="$(fetch_credentials)"; then
		problem credentials-unreadable normal "CLIProxyAPI monitor cannot read credentials" "GET $credentials_url failed. Check the management key file $key_file."
		keep_problems $'credential\t'
		return 0
	fi
	while IFS=$'\t' read -r name status message; do
		problem "credential"$'\t'"$name"$'\t'"$status"$'\t'"$message" \
			normal "CLIProxyAPI credential needs a re-login" "$name: $status ($message)"
	done < <(credential_problems <<<"$body")
}

# check_backup notifies when config.yaml differs from the last successful
# backup. An edit younger than the grace period is skipped, because the
# backup service waits for a quiet period before it uploads.
check_backup() {
	local current recorded=''
	[[ -f "$config_file" ]] || return 0
	if (($(date +%s) - $(stat -c %Y "$config_file") < backup_grace)); then
		return 0
	fi
	current="$(sha256sum "$config_file" | cut -d' ' -f1)"
	[[ -f "$backup_record" ]] && recorded="$(sed -n 's/^sha256=//p' "$backup_record")"
	[[ "$current" == "$recorded" ]] && return 0
	problem backup normal "CLIProxyAPI config not backed up" "$config_file changed since the last successful backup. See: journalctl --user -u cli-proxy-api-config-backup.service"
}

# wasted_quota_resets reads window-reset log lines on stdin and prints one
# "credential<TAB>window<TAB>share_left<TAB>reset_at" line per longest
# window (kind=ranking) that reset with more than the threshold left. The
# credential is the text between "credential=" and " provider=", because it
# is the only value that can contain a space.
wasted_quota_resets() {
	awk -v threshold="$wasted_threshold" '
		function value(key,    rest, end) {
			rest = substr(line, index(line, " " key "=") + length(key) + 2)
			end = index(rest, " ")
			return end ? substr(rest, 1, end - 1) : rest
		}
		{
			start = index($0, "quota window reset | credential=")
			if (!start) next
			line = substr($0, start + length("quota window reset |"))
			rest = substr(line, index(line, "credential=") + length("credential="))
			credential = substr(rest, 1, index(rest, " provider=") - 1)
			if (value("kind") != "ranking" || value("share_left") + 0 <= threshold + 0) next
			printf "%s\t%s\t%s\t%s\n", credential, value("window"), value("share_left"), value("reset_at")
		}'
}

# check_wasted_quota notifies once per credential, window and reset time.
# Alerts already sent are kept in a state file. The file keeps resets from
# the last two journal windows; older resets are ignored, so pruning never
# lets an alert repeat. RFC 3339 UTC times sort as text.
check_wasted_quota() {
	local alerted="$state_dir/wasted-quota-alerted" cutoff credential window share reset_at percent
	cutoff="$(date -u -d "-$((journal_hours * 2)) hours" +%Y-%m-%dT%H:%M:%SZ)"
	mkdir -p "$state_dir"
	touch "$alerted"
	while IFS=$'\t' read -r credential window share reset_at; do
		if [[ "$reset_at" < "$cutoff" ]] ||
			grep -qxF "$credential"$'\t'"$window"$'\t'"$reset_at" "$alerted"; then
			continue
		fi
		percent="$(awk -v s="$share" 'BEGIN { printf "%.0f", s * 100 }')"
		notify normal "CLIProxyAPI wasted quota" "$credential: $window reset at $reset_at with $percent% left"
		printf '%s\t%s\t%s\n' "$credential" "$window" "$reset_at" >>"$alerted"
	done < <(journalctl --user -u "$unit" --since "-${journal_hours}h" -o cat --no-pager -q | wasted_quota_resets)

	awk -F '\t' -v cutoff="$cutoff" '$3 >= cutoff' "$alerted" >"$alerted.tmp"
	mv -f "$alerted.tmp" "$alerted"
}

main() {
	umask 077
	mkdir -p "$state_dir"
	previous_problems="$state_dir/problems"
	current_problems="$(mktemp "$state_dir/problems.XXXXXX")"
	trap 'rm -f "${current_problems:-}"' EXIT
	touch "$previous_problems"

	if check_health; then
		check_credentials
	else
		keep_problems $'credential\t'
	fi
	check_backup
	check_wasted_quota
	mv -f "$current_problems" "$previous_problems"
}

main "$@"
