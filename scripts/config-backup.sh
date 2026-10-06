#!/usr/bin/env bash
# Back up CLIProxyAPI's config.yaml to a document in a 1Password vault.
#
# Run by cli-proxy-api-config-backup.service, which the matching .path unit
# starts when config.yaml changes. The script waits until the file has been
# quiet for a debounce period, uploads it with `op document edit` (or
# `op document create` on the first backup), and on success writes the
# last-success record that the monitor reads. Content equal to the last
# backup is not uploaded again.
#
# Only config.yaml is backed up. Credentials are not, because their refresh
# tokens rotate.
#
# Install (go-live):
#   ln -s "$PWD/scripts/config-backup.sh" ~/.local/bin/cli-proxy-api-config-backup
#   ln -s "$PWD"/scripts/systemd/cli-proxy-api-config-backup.{path,service} ~/.config/systemd/user/
#   # Service account token for the CLIProxyAPI vault only:
#   (umask 077 && cat >~/.config/cli-proxy-api/config-backup.token)
#   systemctl --user daemon-reload
#   systemctl --user enable --now cli-proxy-api-config-backup.path
#   systemctl --user start cli-proxy-api-config-backup.service  # first backup
#
# Settings (environment, all optional; ~/.config and ~/.local/state follow
# XDG_CONFIG_HOME and XDG_STATE_HOME when set):
#   CONFIG_BACKUP_FILE              config to back up
#                                   (default ~/.config/cli-proxy-api/config.yaml)
#   CONFIG_BACKUP_TOKEN_FILE        1Password service account token, mode 0600
#                                   (default ~/.config/cli-proxy-api/config-backup.token)
#   CONFIG_BACKUP_VAULT             vault (default CLIProxyAPI)
#   CONFIG_BACKUP_DOCUMENT          document item title (default config.yaml)
#   CONFIG_BACKUP_DEBOUNCE_SECONDS  quiet period before upload (default 10)
#   CONFIG_BACKUP_RECORD            last-success record
#                                   (default ~/.local/state/cli-proxy-api/config-backup-last-success)
#
# Last-success record format (key=value lines, rewritten atomically):
#   time=<UTC time of the successful upload, RFC 3339, e.g. 2026-10-06T12:00:00Z>
#   epoch=<the same time in Unix seconds>
#   sha256=<SHA-256 hex digest of the config.yaml content that was uploaded>
# The backup is stale when the record is missing or its sha256 differs from
# `sha256sum config.yaml`. A fresh edit is stale for the debounce period plus
# the upload time, so a checker should allow a few minutes of grace.
#
# Logs go to stderr (the journal). Never print the token or the config
# content: both are secrets.
set -euo pipefail

config_home="${XDG_CONFIG_HOME:-$HOME/.config}"
state_home="${XDG_STATE_HOME:-$HOME/.local/state}"
config_file="${CONFIG_BACKUP_FILE:-$config_home/cli-proxy-api/config.yaml}"
token_file="${CONFIG_BACKUP_TOKEN_FILE:-$config_home/cli-proxy-api/config-backup.token}"
vault="${CONFIG_BACKUP_VAULT:-CLIProxyAPI}"
document="${CONFIG_BACKUP_DOCUMENT:-config.yaml}"
debounce="${CONFIG_BACKUP_DEBOUNCE_SECONDS:-10}"
record="${CONFIG_BACKUP_RECORD:-$state_home/cli-proxy-api/config-backup-last-success}"

log() {
	printf 'config-backup: %s\n' "$*" >&2
}

file_sha256() {
	sha256sum "$1" | cut -d' ' -f1
}

# wait_for_quiet returns once the config content is unchanged across one
# debounce period, so a burst of writes leads to one upload.
wait_for_quiet() {
	local before after
	after="$(file_sha256 "$config_file")"
	while :; do
		before=$after
		sleep "$debounce"
		after="$(file_sha256 "$config_file")"
		[[ "$after" == "$before" ]] && return 0
	done
}

# recorded_sha256 prints the digest in the last-success record, if any.
recorded_sha256() {
	[[ -f "$record" ]] || return 0
	sed -n 's/^sha256=//p' "$record"
}

write_record() {
	local sha=$1 now tmp
	now="$(date -u +%s)"
	mkdir -p "$(dirname "$record")"
	tmp="$(mktemp "$record.XXXXXX")"
	printf 'time=%s\nepoch=%s\nsha256=%s\n' \
		"$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ)" "$now" "$sha" >"$tmp"
	mv -f "$tmp" "$record"
}

# read_token prints the service account token after checking that only the
# owner can read the token file.
read_token() {
	local mode
	if [[ ! -f "$token_file" ]]; then
		log "token file $token_file not found"
		return 1
	fi
	mode="$(stat -c %a "$token_file")"
	if (((8#$mode & 8#077) != 0)); then
		log "token file $token_file must be mode 0600 (is $mode)"
		return 1
	fi
	printf '%s' "$(<"$token_file")"
}

# upload sends the snapshot to 1Password: it edits the document, or creates
# it on the first backup. The existence check lists item metadata only, and
# a failed check fails the upload, so a network error never creates a
# duplicate document. On failure it logs op's message with the token
# redacted.
upload() {
	local snapshot=$1 items found=0
	items="$(run_op item list --vault "$vault" --categories Document --format json)" || return 1
	jq -e --arg title "$document" 'any(.[]; .title == $title)' <<<"$items" >/dev/null 2>&1 || found=$?
	case $found in
	0)
		run_op document edit "$document" "$snapshot" --vault "$vault" --file-name config.yaml >/dev/null
		;;
	1)
		log "document $document not found in vault $vault; creating it"
		run_op document create "$snapshot" --vault "$vault" --title "$document" \
			--file-name config.yaml >/dev/null
		;;
	*)
		log "backup failed; last-success record unchanged; could not read the item list from op"
		return 1
		;;
	esac
}

# run_op runs op with the service account token in its environment. Its
# stdout passes through; on failure it logs op's stderr with the token
# redacted.
run_op() {
	local err
	if OP_SERVICE_ACCOUNT_TOKEN="$token" op "$@" 2>"$work/op.err"; then
		return 0
	fi
	err="$(<"$work/op.err")"
	log "backup failed; last-success record unchanged; op $1 $2 said: ${err//"$token"/[redacted]}"
	return 1
}

main() {
	umask 077
	local snapshot sha

	# Globals used by run_op: token and work.
	token="$(read_token)" || return 1
	work="$(mktemp -d)"
	trap 'rm -rf "${work:-}"' EXIT
	snapshot="$work/config.yaml"

	wait_for_quiet
	while :; do
		cp "$config_file" "$snapshot"
		sha="$(file_sha256 "$snapshot")"
		if [[ "$sha" == "$(recorded_sha256)" ]]; then
			log "config unchanged since last backup (sha256 $sha); nothing to do"
			return 0
		fi
		upload "$snapshot" || return 1
		write_record "$sha"
		log "backed up $config_file to $vault/$document (sha256 $sha)"

		# A change while this service runs does not retrigger the .path unit,
		# so back it up in this run.
		[[ "$(file_sha256 "$config_file")" == "$sha" ]] && return 0
		log "config changed during upload; backing up again"
		wait_for_quiet
	done
}

main "$@"
