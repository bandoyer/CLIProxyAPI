#!/usr/bin/env bash
# Deploy a new CLIProxyAPI build to the systemd user service.
#
# Builds the server from main with version ldflags, keeps the previous binary
# as <bin>.prev, installs the management panel built from the panel fork,
# restarts the service and checks /healthz. See scripts/README.md for install
# and usage.
set -euo pipefail

SELF="$(readlink -f "${BASH_SOURCE[0]}")"
REPO="${CLI_PROXY_API_REPO:-$(dirname "$(dirname "$SELF")")}"
BIN="${CLI_PROXY_API_BIN:-$HOME/.local/bin/cli-proxy-api}"
UNIT="${CLI_PROXY_API_UNIT:-cli-proxy-api.service}"
HEALTH_URL="${CLI_PROXY_API_HEALTH_URL:-http://127.0.0.1:8317/healthz}"
HEALTH_ATTEMPTS="${CLI_PROXY_API_HEALTH_ATTEMPTS:-30}"
PANEL_REPO="${CLI_PROXY_API_PANEL_REPO:-$HOME/Work/Cli-Proxy-API-Management-Center}"
STATIC_DIR="${CLI_PROXY_API_STATIC_DIR:-$HOME/.config/cli-proxy-api/static}"
CONFIG="${CLI_PROXY_API_CONFIG:-$HOME/.config/cli-proxy-api/config.yaml}"
BUN="${CLI_PROXY_API_BUN:-bun}"

log() {
	echo "deploy: $*" >&2
}

warn() {
	log "warning: $*"
}

# install_panel builds the single-file management.html from the panel fork's
# main and installs it into the proxy's static directory. A problem with the
# panel is only a warning: the proxy deploy continues with the page that is
# already installed.
install_panel() {
	local branch commit
	if [[ ! -d "$PANEL_REPO/.git" ]]; then
		warn "panel checkout $PANEL_REPO not found; skipping the panel"
		return 0
	fi
	branch="$(git -C "$PANEL_REPO" branch --show-current)"
	if [[ "$branch" != "main" ]]; then
		warn "panel checkout $PANEL_REPO is on '${branch:-detached HEAD}'; switch it to main to deploy the panel"
		return 0
	fi
	if ! command -v "$BUN" >/dev/null 2>&1; then
		warn "$BUN not found; install bun (mise use -g bun) to deploy the panel"
		return 0
	fi
	log "updating main in $PANEL_REPO"
	if ! git -C "$PANEL_REPO" pull --ff-only --quiet; then
		warn "git pull failed in $PANEL_REPO; skipping the panel"
		return 0
	fi
	commit="$(git -C "$PANEL_REPO" rev-parse --short HEAD)"
	log "building the panel ($commit)"
	if ! (cd "$PANEL_REPO" && "$BUN" install --frozen-lockfile && "$BUN" run build) >&2 ||
		[[ ! -s "$PANEL_REPO/dist/index.html" ]]; then
		warn "panel build failed; keeping the installed management.html"
		return 0
	fi
	mkdir -p "$STATIC_DIR"
	cp "$PANEL_REPO/dist/index.html" "$STATIC_DIR/management.html.new"
	mv -f "$STATIC_DIR/management.html.new" "$STATIC_DIR/management.html"
	log "installed panel $commit as $STATIC_DIR/management.html"
	if [[ -e "$CONFIG" ]] && ! grep -Eq '^[[:space:]]*disable-auto-update-panel:[[:space:]]*true' "$CONFIG"; then
		warn "set disable-auto-update-panel: true in $CONFIG, or the proxy can replace the installed panel"
	fi
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

	install_panel

	log "restarting $UNIT"
	if restart && healthy; then
		log "healthy: $(startup_line)"
		return 0
	fi
	rollback "$version"
	return 1
}

main "$@"
