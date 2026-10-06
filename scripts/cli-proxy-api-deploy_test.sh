#!/usr/bin/env bash
# Tests for cli-proxy-api-deploy.sh.
#
# The deploy script runs against throwaway git repositories (the proxy and the
# panel fork) and stub commands (go, bun, systemctl, curl, journalctl,
# notify-send, sleep) placed first on PATH, so no real service, binary,
# panel or notification is touched.
#
# Run: scripts/cli-proxy-api-deploy_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$SCRIPT_DIR/cli-proxy-api-deploy.sh"

failures=0
current_test=""

fail() {
	echo "FAIL: $current_test: $*" >&2
	failures=$((failures + 1))
}

assert_contains() {
	local file="$1" needle="$2"
	if ! grep -qF -- "$needle" "$file" 2>/dev/null; then
		fail "expected $(basename "$file") to contain '$needle'; got: $(cat "$file" 2>/dev/null)"
	fi
}

assert_not_contains() {
	local file="$1" needle="$2"
	if grep -qF -- "$needle" "$file" 2>/dev/null; then
		fail "expected $(basename "$file") not to contain '$needle'"
	fi
}

# assert_before checks that the first line with $2 comes before the first
# line with $3.
assert_before() {
	local file="$1" first="$2" second="$3" a b
	a="$(grep -nF -- "$first" "$file" | head -n 1 | cut -d: -f1)"
	b="$(grep -nF -- "$second" "$file" | head -n 1 | cut -d: -f1)"
	if [[ -z "$a" || -z "$b" || "$a" -ge "$b" ]]; then
		fail "expected '$first' before '$second' in $(basename "$file"); got: $(cat "$file")"
	fi
}

assert_count() {
	local file="$1" needle="$2" want="$3" got
	got="$(grep -cF -- "$needle" "$file" 2>/dev/null || true)"
	if [[ "$got" != "$want" ]]; then
		fail "expected $want lines with '$needle' in $(basename "$file"), got $got"
	fi
}

# commit_panel commits a change to the panel fork in clone $1 and pushes it.
commit_panel() {
	local clone="$1" message="$2"
	echo "$message" >>"$clone/src.ts"
	git -C "$clone" add src.ts
	git -C "$clone" -c user.name=t -c user.email=t@example.com commit -q -m "$message"
	git -C "$clone" push -q origin main 2>/dev/null
}

# setup builds a sandbox: a bare origin, a clone on main tagged v1.2.3, the
# same for the panel fork, stub commands, a config with panel auto-update
# off, and install directories. It sets the globals used by the tests.
setup() {
	SANDBOX="$(mktemp -d)"
	STATE="$SANDBOX/state"
	STUBS="$SANDBOX/stubs"
	REPO="$SANDBOX/repo"
	BIN="$SANDBOX/install/cli-proxy-api"
	PANEL="$SANDBOX/panel"
	STATIC="$SANDBOX/config/static"
	CONFIG="$SANDBOX/config/config.yaml"
	mkdir -p "$STATE" "$STUBS" "$SANDBOX/install" "$SANDBOX/config"
	printf 'management:\n  disable-auto-update-panel: true\n' >"$CONFIG"

	git init -q --bare -b main "$SANDBOX/panel-origin.git"
	git clone -q "$SANDBOX/panel-origin.git" "$PANEL" 2>/dev/null
	git -C "$PANEL" switch -q -c main 2>/dev/null || true
	commit_panel "$PANEL" init
	git -C "$PANEL" push -q -u origin main 2>/dev/null

	git init -q --bare -b main "$SANDBOX/origin.git"
	git clone -q "$SANDBOX/origin.git" "$REPO" 2>/dev/null
	git -C "$REPO" switch -q -c main 2>/dev/null || true
	echo "package main" >"$REPO/main.go"
	git -C "$REPO" add main.go
	git -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q -m init
	git -C "$REPO" tag v1.2.3
	git -C "$REPO" push -q -u origin main 2>/dev/null

	# go stub: writes a fake server binary that prints the startup log line
	# built from the -X ldflags and exits with FAKE_BUILD_EXIT (0 = healthy).
	cat >"$STUBS/go" <<'EOF'
#!/usr/bin/env bash
echo "go $*" >>"$STATE/calls"
out="" ldflags=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	-o) out="$2"; shift 2 ;;
	-ldflags) ldflags="$2"; shift 2 ;;
	*) shift ;;
	esac
done
[[ -n "$out" ]] || exit 2
ver="$(sed -n "s/.*-X 'main.Version=\([^']*\)'.*/\1/p" <<<"$ldflags")"
commit="$(sed -n "s/.*-X 'main.Commit=\([^']*\)'.*/\1/p" <<<"$ldflags")"
built="$(sed -n "s/.*-X 'main.BuildDate=\([^']*\)'.*/\1/p" <<<"$ldflags")"
cat >"$out" <<BIN
#!/bin/sh
echo "CLIProxyAPI Version: $ver, Commit: $commit, BuiltAt: $built"
exit ${FAKE_BUILD_EXIT:-0}
BIN
chmod +x "$out"
EOF

	# systemctl stub: "restart" records which binary is now running.
	cat >"$STUBS/systemctl" <<'EOF'
#!/usr/bin/env bash
echo "systemctl $*" >>"$STATE/calls"
if [[ "$*" == *restart* ]]; then
	cp "$CLI_PROXY_API_BIN" "$STATE/running"
fi
EOF

	# curl stub: the health check passes when the running binary exits 0.
	cat >"$STUBS/curl" <<'EOF'
#!/usr/bin/env bash
echo "curl $*" >>"$STATE/calls"
[[ -x "$STATE/running" ]] && "$STATE/running" >/dev/null
EOF

	# journalctl stub: the running binary's output stands in for its log.
	cat >"$STUBS/journalctl" <<'EOF'
#!/usr/bin/env bash
"$STATE/running" || true
EOF

	cat >"$STUBS/notify-send" <<'EOF'
#!/usr/bin/env bash
echo "notify-send $*" >>"$STATE/notifications"
EOF

	# bun stub: "run build" writes the single-file page for the checkout's
	# commit, or every call fails with FAKE_BUN_EXIT.
	cat >"$STUBS/bun" <<'EOF'
#!/usr/bin/env bash
echo "bun $* (in $PWD)" >>"$STATE/calls"
[[ "${FAKE_BUN_EXIT:-0}" == 0 ]] || exit "$FAKE_BUN_EXIT"
if [[ "$*" == "run build" ]]; then
	mkdir -p dist
	echo "<html>panel $(git rev-parse --short HEAD)</html>" >dist/index.html
fi
EOF

	cat >"$STUBS/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

	chmod +x "$STUBS"/*
	touch "$STATE/calls" "$STATE/notifications"
}

teardown() {
	rm -rf "$SANDBOX"
}

# install_previous puts a healthy "previous" binary in place.
install_previous() {
	cat >"$BIN" <<'EOF'
#!/bin/sh
echo "CLIProxyAPI Version: v1.0.0, Commit: old, BuiltAt: then"
exit 0
EOF
	chmod +x "$BIN"
}

run_deploy() {
	set +e
	PATH="$STUBS:$PATH" STATE="$STATE" \
		CLI_PROXY_API_REPO="$REPO" \
		CLI_PROXY_API_BIN="$BIN" \
		CLI_PROXY_API_HEALTH_ATTEMPTS=3 \
		CLI_PROXY_API_PANEL_REPO="${PANEL_REPO_OVERRIDE:-$PANEL}" \
		CLI_PROXY_API_STATIC_DIR="$STATIC" \
		CLI_PROXY_API_CONFIG="$CONFIG" \
		CLI_PROXY_API_BUN="${BUN_OVERRIDE:-bun}" \
		"$DEPLOY" "$@" >"$STATE/out" 2>&1
	DEPLOY_EXIT=$?
	set -e
}

test_successful_deploy_runs_new_build_with_version_and_commit() {
	install_previous
	run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	local commit
	commit="$(git -C "$REPO" rev-parse --short HEAD)"
	"$BIN" >"$STATE/new-version" || true
	assert_contains "$STATE/new-version" "Version: v1.2.3, Commit: $commit"
	assert_contains "$BIN.prev" "Version: v1.0.0"
	assert_contains "$STATE/calls" "systemctl --user restart cli-proxy-api.service"
	assert_contains "$STATE/calls" "http://127.0.0.1:8317/healthz"
	assert_contains "$STATE/out" "CLIProxyAPI Version: v1.2.3, Commit: $commit"
	assert_not_contains "$STATE/notifications" "critical"
}

test_failed_health_check_restores_previous_binary_and_notifies() {
	install_previous
	FAKE_BUILD_EXIT=1 run_deploy

	[[ "$DEPLOY_EXIT" != 0 ]] || fail "expected non-zero exit"
	"$BIN" >"$STATE/restored-version" || true
	assert_contains "$STATE/restored-version" "Version: v1.0.0, Commit: old"
	assert_contains "$STATE/running" "Version: v1.0.0"
	assert_count "$STATE/calls" "systemctl --user restart cli-proxy-api.service" 2
	assert_count "$STATE/notifications" "-u critical" 1
	assert_contains "$STATE/notifications" "rolled back to v1.0.0"
}

test_deploy_builds_latest_main_from_origin() {
	install_previous
	local other="$SANDBOX/other"
	git clone -q "$SANDBOX/origin.git" "$other" 2>/dev/null
	echo "// merged PR" >>"$other/main.go"
	git -C "$other" -c user.name=t -c user.email=t@example.com commit -q -am "merged PR"
	git -C "$other" push -q origin main 2>/dev/null
	local merged
	merged="$(git -C "$other" rev-parse --short HEAD)"

	run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	"$BIN" >"$STATE/new-version" || true
	assert_contains "$STATE/new-version" "Commit: $merged"
}

test_deploy_refuses_a_checkout_not_on_main() {
	install_previous
	git -C "$REPO" switch -q -c feature

	run_deploy

	[[ "$DEPLOY_EXIT" != 0 ]] || fail "expected non-zero exit"
	assert_contains "$STATE/out" "main"
	assert_not_contains "$STATE/calls" "go build"
	assert_not_contains "$STATE/calls" "restart"
	"$BIN" >"$STATE/version" || true
	assert_contains "$STATE/version" "Version: v1.0.0"
}

# install_previous_panel puts an already installed page in place.
install_previous_panel() {
	mkdir -p "$STATIC"
	echo "<html>previous panel</html>" >"$STATIC/management.html"
}

test_deploy_installs_the_built_panel_before_the_restart() {
	install_previous
	install_previous_panel
	run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATIC/management.html" "panel $(git -C "$PANEL" rev-parse --short HEAD)"
	assert_contains "$STATE/calls" "bun install --frozen-lockfile (in $PANEL)"
	assert_before "$STATE/calls" "bun run build" "systemctl --user restart"
	assert_not_contains "$STATE/out" "warning"
}

test_deploy_builds_the_latest_panel_main_from_its_origin() {
	install_previous
	local other="$SANDBOX/panel-other"
	git clone -q "$SANDBOX/panel-origin.git" "$other" 2>/dev/null
	commit_panel "$other" "merged panel PR"

	run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATIC/management.html" "panel $(git -C "$other" rev-parse --short HEAD)"
}

test_missing_panel_checkout_warns_and_still_deploys_the_proxy() {
	install_previous
	PANEL_REPO_OVERRIDE="$SANDBOX/no-such-panel" run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATE/out" "warning: panel checkout $SANDBOX/no-such-panel not found"
	assert_not_contains "$STATE/calls" "bun "
	assert_contains "$STATE/calls" "systemctl --user restart cli-proxy-api.service"
	"$BIN" >"$STATE/new-version" || true
	assert_contains "$STATE/new-version" "Version: v1.2.3"
}

test_panel_checkout_not_on_main_is_skipped_with_a_warning() {
	install_previous
	install_previous_panel
	git -C "$PANEL" switch -q -c feature

	run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATE/out" "warning:"
	assert_contains "$STATE/out" "switch it to main"
	assert_not_contains "$STATE/calls" "bun "
	assert_contains "$STATIC/management.html" "previous panel"
}

test_failed_panel_build_keeps_the_installed_page_and_deploys_the_proxy() {
	install_previous
	install_previous_panel
	FAKE_BUN_EXIT=1 run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATE/out" "warning: panel build failed"
	assert_contains "$STATIC/management.html" "previous panel"
	assert_contains "$STATE/calls" "systemctl --user restart cli-proxy-api.service"
}

test_missing_bun_skips_the_panel_with_a_warning() {
	install_previous
	BUN_OVERRIDE="$SANDBOX/no-such-bun" run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATE/out" "warning: $SANDBOX/no-such-bun not found"
	[[ ! -e "$STATIC/management.html" ]] || fail "expected no installed panel"
}

test_warns_when_panel_auto_update_is_not_disabled() {
	install_previous
	printf 'management:\n  disable-auto-update-panel: false\n' >"$CONFIG"

	run_deploy

	[[ "$DEPLOY_EXIT" == 0 ]] || fail "exit $DEPLOY_EXIT, output: $(cat "$STATE/out")"
	assert_contains "$STATE/out" "warning:"
	assert_contains "$STATE/out" "disable-auto-update-panel: true"
}

for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
	current_test="$t"
	setup
	"$t"
	teardown
done

if [[ "$failures" -gt 0 ]]; then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "ok"
