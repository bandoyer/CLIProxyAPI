#!/usr/bin/env bash
# Print a routing report for the last N hours of the CLIProxyAPI journal.
#
# Usage: cli-proxy-api-routing-report [HOURS]   (default 24)
#
# The report has three summaries, read from three log lines of the proxy:
#   - wasted quota per credential and window ("quota window reset | ...");
#   - binding moves per thread with reasons ("affinity binding ended | ...",
#     with the new credential from the pick log);
#   - picks per credential by reason ("expiring-first pick | ...").
# Readings live in memory only, so resets after a restart show up only once
# a new reading has arrived.
#
# Settings (environment, optional):
#   CLI_PROXY_API_UNIT   journald unit (default cli-proxy-api.service)
set -euo pipefail

unit="${CLI_PROXY_API_UNIT:-cli-proxy-api.service}"

usage() {
	printf 'usage: %s [HOURS]\n' "${0##*/}" >&2
}

# table aligns tab-separated rows into columns, or prints "none" when there
# are no rows.
table() {
	local rows
	rows="$(cat)"
	if [[ -z "$rows" ]]; then
		echo '  none'
		return 0
	fi
	column -t -s $'\t' <<<"$rows"
}

# Each summary below reads `journalctl -o cat` lines on stdin and prints
# tab-separated rows, header first, or nothing when it finds no log line.
# The log values are space-separated key=value pairs in a fixed order. The
# credential (an auth file name) and the thread can contain spaces, so they
# are taken as the text up to the next key.

# wasted_quota prints one row per credential and window from the
# window-reset log lines on stdin: the number of resets, the total share
# left at those resets (in % of one window), and the share left at the
# latest reset.
wasted_quota() {
	awk '
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
			key = credential "\t" value("window")
			if (!(key in resets)) order[++n] = key
			resets[key]++
			total[key] += value("share_left")
			kind[key] = value("kind")
			if (value("reset_at") >= latest_at[key]) {
				latest_at[key] = value("reset_at")
				latest[key] = value("share_left")
			}
		}
		END {
			if (!n) exit
			print "CREDENTIAL\tWINDOW\tKIND\tRESETS\tWASTED\tLAST"
			for (i = 1; i <= n; i++) {
				key = order[i]
				printf "%s\t%s\t%d\t%.0f%%\t%.0f%%\n", key, kind[key], resets[key], total[key] * 100, latest[key] * 100
			}
		}' | table
}

# binding_moves prints one row per ended binding, grouped by thread: the
# credential the thread left, the one it moved to, and the reason. The new
# credential comes from the pick log. When a request finds its bound
# credential unusable, the re-pick line comes first and the binding-end line
# follows with the same request ID. When a failed request ends the binding,
# the end line has no request ID and the thread's next pick names the new
# credential. "?" means no pick followed yet. A pick with no binding-end line
# before it (a new thread, or a binding that expired when idle) is not a move.
binding_moves() {
	awk '
		# request_id prints the ID in the second [...] of the formatter prefix,
		# or "" for none.
		function request_id(    rest, id) {
			if (substr($0, 1, 1) != "[") return ""
			rest = substr($0, index($0, "] [") + 3)
			id = substr(rest, 1, index(rest, "]") - 1)
			return id ~ /^-+$/ ? "" : id
		}
		# between prints the text after "key=" up to " next_key=", or to the
		# end of the line when next_key is "".
		function between(line, key, next_key,    rest) {
			rest = substr(line, index(line, key "=") + length(key) + 1)
			return next_key == "" ? rest : substr(rest, 1, index(rest, " " next_key "=") - 1)
		}
		{
			if (start = index($0, "expiring-first pick | credential=")) {
				line = substr($0, start + length("expiring-first pick | "))
				thread = between(line, "thread", "provider")
				credential = between(line, "credential", "urgency")
				if (thread in pending) {
					to[pending[thread]] = credential
					delete pending[thread]
				}
				last_pick_request[thread] = request_id()
				last_pick_credential[thread] = credential
				next
			}
			if (start = index($0, "affinity binding ended | thread=")) {
				line = substr($0, start + length("affinity binding ended | "))
				thread = between(line, "thread", "credential")
				credential = between(line, "credential", "provider")
				if (!(thread in moves)) threads[++thread_count] = thread
				m = ++move_count
				move_list[thread, ++moves[thread]] = m
				from[m] = credential
				reason[m] = between(line, "reason", "")
				to[m] = "?"
				request = request_id()
				if (request != "" && last_pick_request[thread] == request && last_pick_credential[thread] != credential) {
					to[m] = last_pick_credential[thread]
				} else {
					pending[thread] = m
				}
			}
		}
		END {
			if (!move_count) exit
			print "THREAD\tMOVE\tFROM\tTO\tREASON"
			for (i = 1; i <= thread_count; i++) {
				thread = threads[i]
				for (j = 1; j <= moves[thread]; j++) {
					m = move_list[thread, j]
					printf "%s\t%d\t%s\t%s\t%s\n", thread, j, from[m], to[m], reason[m]
				}
			}
		}' | table
}

# picks prints one row per credential from the expiring-first pick log: the
# number of picks, then the count for each known reason. Reason values are
# open-ended, so picks with any other reason are counted under OTHER.
picks() {
	awk '
		BEGIN { split("more_urgent no_data binding_kept credit_only", reasons, " ") }
		{
			start = index($0, "expiring-first pick | credential=")
			if (!start) next
			line = substr($0, start + length("expiring-first pick | credential="))
			credential = substr(line, 1, index(line, " urgency=") - 1)
			rest = substr(line, index(line, " reason=") + length(" reason="))
			reason = substr(rest, 1, index(rest, " ") - 1)
			if (!(credential in total)) order[++n] = credential
			total[credential]++
			count[credential, reason]++
		}
		END {
			if (!n) exit
			print "CREDENTIAL\tPICKS\tMORE_URGENT\tNO_DATA\tBINDING_KEPT\tCREDIT_ONLY\tOTHER"
			for (i = 1; i <= n; i++) {
				credential = order[i]
				printf "%s\t%d", credential, total[credential]
				other = total[credential]
				for (r = 1; r <= 4; r++) {
					printf "\t%d", count[credential, reasons[r]]
					other -= count[credential, reasons[r]]
				}
				printf "\t%d\n", other
			}
		}' | table
}

main() {
	local hours=${1:-24} journal
	if [[ ! "$hours" =~ ^[1-9][0-9]*$ ]] || (($# > 1)); then
		usage
		return 2
	fi
	journal="$(journalctl --user -u "$unit" --since "-${hours}h" -o cat --no-pager -q)"

	printf 'CLIProxyAPI routing report for the last %s hours (%s)\n' "$hours" "$unit"
	printf '\nWasted quota per credential and window\n'
	wasted_quota <<<"$journal"
	printf '\nBinding moves per thread\n'
	binding_moves <<<"$journal"
	printf '\nPicks per credential\n'
	picks <<<"$journal"
}

main "$@"
