#!/usr/bin/env bash
# Deploy a new CLIProxyAPI build to the systemd user service.
#
# Builds the server from main with version ldflags, keeps the previous binary
# as <bin>.prev, restarts the service and checks /healthz. See
# scripts/README.md for install and usage.
set -euo pipefail

SELF="$(readlink -f "${BASH_SOURCE[0]}")"
REPO="${CLI_PROXY_API_REPO:-$(dirname "$(dirname "$SELF")")}"
BIN="${CLI_PROXY_API_BIN:-$HOME/.local/bin/cli-proxy-api}"
UNIT="${CLI_PROXY_API_UNIT:-cli-proxy-api.service}"
HEALTH_URL="${CLI_PROXY_API_HEALTH_URL:-http://127.0.0.1:8317/healthz}"
HEALTH_ATTEMPTS="${CLI_PROXY_API_HEALTH_ATTEMPTS:-30}"

log() {
	echo "deploy: $*" >&2
}

# build compiles the server into $1 with the same -X ldflags as the Dockerfile.
build() {
	local out="$1" version commit build_date
	version="$(git -C "$REPO" describe --tags --always --dirty)"
	commit="$(git -C "$REPO" rev-parse --short HEAD)"
	build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	log "building $version ($commit)"
	(cd "$REPO" && go build -buildvcs=false \
		-ldflags "-s -w -X 'main.Version=$version' -X 'main.Commit=$commit' -X 'main.BuildDate=$build_date'" \
		-o "$out" ./cmd/server)
}

# healthy polls the health URL once per second until it answers or the
# attempts run out.
healthy() {
	local i
	for ((i = 1; i <= HEALTH_ATTEMPTS; i++)); do
		if curl -fsS --max-time 2 -o /dev/null "$HEALTH_URL"; then
			return 0
		fi
		sleep 1
	done
	return 1
}

restart() {
	systemctl --user restart "$UNIT"
}

# startup_line prints the version line the server logged at its latest start.
startup_line() {
	journalctl --user -u "$UNIT" -n 200 -o cat --no-pager 2>/dev/null |
		grep 'CLIProxyAPI Version:' | tail -n 1 || true
}

# startup_version prints "<version> (<commit>)" from the latest startup line.
startup_version() {
	startup_line | sed -n 's/.*CLIProxyAPI Version: \([^,]*\), Commit: \([^,]*\),.*/\1 (\2)/p'
}

notify_critical() {
	log "$1: $2"
	notify-send -u critical -a cli-proxy-api "$1" "$2" || log "notify-send failed"
}

# rollback restores the previous binary, keeps the failed one as
# <bin>.failed, restarts the service and sends a critical notification.
rollback() {
	local failed_version="$1"
	if [[ ! -e "$BIN.prev" ]]; then
		notify_critical "CLIProxyAPI deploy failed" \
			"$failed_version failed its health check and there is no previous binary to restore"
		return
	fi
	mv -f "$BIN" "$BIN.failed"
	cp -p "$BIN.prev" "$BIN"
	log "restarting $UNIT on the previous binary"
	if restart && healthy; then
		notify_critical "CLIProxyAPI deploy rolled back" \
			"$failed_version failed its health check; rolled back to $(startup_version)"
	else
		notify_critical "CLIProxyAPI deploy failed" \
			"$failed_version failed its health check and the rolled-back binary is unhealthy too"
	fi
}

main() {
	local branch version
	branch="$(git -C "$REPO" branch --show-current)"
	if [[ "$branch" != "main" ]]; then
		log "$REPO is on '${branch:-detached HEAD}'; switch it to main first"
		return 1
	fi
	log "updating main in $REPO"
	git -C "$REPO" pull --ff-only --quiet
	version="$(git -C "$REPO" describe --tags --always --dirty)"

	mkdir -p "$(dirname "$BIN")"
	build "$BIN.new"

	if [[ -e "$BIN" ]]; then
		cp -p "$BIN" "$BIN.prev"
	fi
	mv -f "$BIN.new" "$BIN"

	log "restarting $UNIT"
	if restart && healthy; then
		log "healthy: $(startup_line)"
		return 0
	fi
	rollback "$version"
	return 1
}

main "$@"
