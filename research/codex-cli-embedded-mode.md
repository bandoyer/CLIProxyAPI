# Does selecting the proxy with `codex -c` cost anything in the Codex TUI?

Research for issue #23 (map #2). It follows the client setup decided in "How does each client connect to the proxy?" (#15): the Codex CLI selects the `cliproxy` provider with `alias codex='codex -c model_provider=cliproxy'`, T3 Code passes the same `-c` to the `codex app-server` that it launches, and the ChatGPT desktop app stays direct.

## Short answer

Yes, but the cost is small and does not touch routing, history, login, or what the proxy sees.

- Any `-c` override other than a short list of feature flags puts the Codex 0.160.0 TUI in **embedded mode**: the TUI runs its own in-process app server instead of attaching to the **shared background server** (the managed `codex app-server` daemon under `~/.codex`).
- In embedded mode, the proxied CLI loses three things: "Run in background" when you press Ctrl+C during a running task, the `codex agents` dashboard and the `/daemon` menu, and remote control of those sessions from another device. Threads are still written to `~/.codex`, so resume, history, and the shared ChatGPT login work as before.
- T3 Code and the ChatGPT desktop app are not affected. Each one already starts its own `codex app-server` and never uses the shared background server.
- No other way to select `cliproxy` for the CLI only keeps the shared server without a larger cost. Profiles also force embedded mode, no environment variable selects the provider, the daemon cannot be started with a provider override, and a separate `CODEX_HOME` means a second daemon, a second config, and split history.
- **Recommendation:** keep `-c model_provider=cliproxy` and add `--no-daemon` to the alias, which removes the startup warning without changing behaviour. The client-setup decision does not need to change; the three losses become accepted losses, and the existing bypass (`command codex`) keeps the shared server when it is needed.

## Sources and versions

| Source | Version | Where |
|---|---|---|
| Codex source | tag `rust-v0.160.0` (commit `a956835`) | `github.com/openai/codex`, paths below are under `codex-rs/` |
| Codex binary | `codex-cli 0.160.0` | `~/.local/bin/codex` |
| Running processes on `omarchy-desktop` | 2026-10-04 | `pgrep -af "codex.*app-server"`, `/proc/<pid>/environ` (variable names only) |

Source links use `https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/<path>#L<n>`; the table cites `<path>:<lines>`.

## (a) What the shared background server provides

### How the TUI chooses a server

1. At startup the TUI computes a **daemon exclusion**. These inputs exclude the shared server ([`tui/src/daemon_startup.rs:25-52`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/daemon_startup.rs#L25-L52)): `--no-daemon`, `--oss`, workload identity, `CODEX_EXEC_SERVER_URL`, `--profile`, and then [`config_exclusion`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/daemon_startup.rs#L54-L89).
2. `config_exclusion` accepts a `-c` override only when it is a boolean under `features.*` for an allow-listed feature, `tui.fullscreen_transcript`, or `suppress_unstable_features_warning` (lines 60-77, allow list at lines 91-104). Anything else, including `model_provider=cliproxy`, returns the reason "command-line configuration overrides (-c, --enable, --disable, or --search)" (line 79).
3. With no exclusion, the TUI probes the daemon socket under `CODEX_HOME` ([`tui/src/startup_orchestration.rs:302-315`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L302-L315), [`tui/src/lib.rs:513-534`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/lib.rs#L513-L534)) and, because `daemon_auto_start` is on by default ([`features/src/lib.rs:946-952`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/features/src/lib.rs#L946-L952)), starts the daemon if it is missing ([`startup_orchestration.rs:494-557`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L494-L557)).
4. With an exclusion, the target is `Embedded` ([`tui/src/lib.rs:1013-1042`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/lib.rs#L1013-L1042)), and the TUI prints "Running without the shared background server: {reason} requires embedded mode." only when auto-start is on ([`startup_orchestration.rs:577-585`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L577-L585)).

The daemon socket is `CODEX_HOME/app-server-control/app-server-control.sock` ([`app-server-transport/src/transport/mod.rs:64-70`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-transport/src/transport/mod.rs#L64-L70)), so there is one shared server per `CODEX_HOME`.

### What attaching to the shared server gives the TUI

| Capability | Shared server | Embedded | Source |
|---|---|---|---|
| Ctrl+C during a running task offers "Run in background" ("Exit Codex and leave the task running") | Yes | No; the prompt is shown only for `LocalDaemon` | [`tui/src/app/input.rs:453-526`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app/input.rs#L453-L526) |
| Agents dashboard (`codex agents`, "Browse all agent sessions on the shared local app-server daemon") lists the session | Yes | No; the TUI shows "This session isn't connected to a shared background server." | [`tui/src/app/agents_overview.rs:78-107`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app/agents_overview.rs#L78-L107); `codex --help` |
| `/daemon` menu (daemon version, updates) | Yes | "Not connected to the local background server." | [`tui/src/app/daemon_menu.rs:29-46`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app/daemon_menu.rs#L29-L46) |
| Remote control of the session from another device (the daemon "backs ... remote clients such as the desktop and mobile apps") | Yes, when remote control is on | No; the session lives in the TUI process | [`app-server-daemon/README.md:6-9`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/README.md?plain=1#L6-L9), lines 166-176 |
| Threads saved under `~/.codex`, resume and fork, history, shared ChatGPT login | Yes | Yes; both modes use the same `CODEX_HOME` | [`startup_orchestration.rs:442-450`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L442-L450) loads the same config and home either way |
| Shared-service features (`code_mode_host`, `auth_elicitation`, `api_key_model_discovery`, `mcp_oauth_refresh_coordination`) | Checked for compatibility with the daemon | Run in-process with the session's own settings | [`daemon_startup.rs:9-14`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/daemon_startup.rs#L9-L14), lines 119-186 |

### What this means for Dan's clients

Processes running on `omarchy-desktop` on 2026-10-04:

| Process | Server it uses | `CLIPROXY_API_KEY` in its environment |
|---|---|---|
| ChatGPT desktop app: `/usr/lib/chatgpt/resources/codex -c features.code_mode_host=true app-server --analytics-default-enabled ...` | Its own app server | No |
| T3 Code: `codex app-server -c model_provider=cliproxy -c mcp_servers.t3-code...` | Its own app server | Yes |
| Shared background server: `~/.codex/packages/standalone/releases/0.160.0-.../codex app-server --remote-control --listen unix:// --managed-daemon`, started 2026-10-01 by its updater loop | — | No |

`~/.codex/app-server-daemon/settings.json` contains `"remoteControlEnabled": true`.

- **One person, several TUI windows.** Each proxied window runs its own embedded server. The windows still share history and login through `~/.codex`. What is lost is the ability to leave a task running after closing a window, and one dashboard listing every window's agents.
- **T3 Code.** Not affected: it never attached to the shared server. It runs `codex app-server` itself, so the TUI's daemon logic does not apply.
- **ChatGPT desktop app.** Not affected: it runs its own bundled `codex app-server`, not the shared server, and it never sees the CLI's `-c`.
- **Remote control.** Dan has remote control on. Proxied CLI sessions are not reachable from another device. Sessions started from another device run on the shared server, which uses the built-in `openai` provider, so they stay direct.

## (b) Other ways to select `cliproxy` for the CLI only

| Option | Keeps the shared server? | Leaves the ChatGPT app and T3 Code unchanged? | Cost |
|---|---|---|---|
| `-c model_provider=cliproxy` (decided) | No | Yes | Losses in (a) |
| `--profile cliproxy` (`~/.codex/cliproxy.config.toml`) | No: `--profile` is itself an exclusion | Yes | Same losses as `-c`; confirmed on the installed binary |
| Legacy `[profiles.x]` with `profile = "x"` | Only if set in `config.toml`, which is global | No: the ChatGPT app reads the same file | — |
| Environment variable that selects the provider or config | None exists | — | — |
| Start the shared server with the override | No: the daemon is launched with feature overrides only | — | — |
| Separate `CODEX_HOME` through a wrapper | Yes, a second shared server | Yes | Second config to keep in sync, split history, a second daemon install |
| `--remote unix://` to the existing shared server, plus `-c model_provider=cliproxy` | Yes, in remote mode | Yes | Daemon lacks the key; remote mode drops local cwd, sandbox, and approval settings; untested |

### Profiles

- In 0.160.0, `--profile`/`-p` "Layer[s] `$CODEX_HOME/<name>.config.toml` on top of the base user config" ([`utils/cli/src/shared_options.rs:34-36`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/utils/cli/src/shared_options.rs#L34-L36)); the TUI sets it as a loader override ([`startup_orchestration.rs:96-101`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L96-L101)).
- `--profile` is an explicit daemon exclusion ([`daemon_startup.rs:42-43`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/daemon_startup.rs#L42-L43)). Observed: `codex --profile qwen38` (Dan's existing local-model profile) shows "Running without the shared background server: --profile requires embedded mode."
- Legacy profiles still exist (`profile` and `[profiles.*]` in [`config/src/config_toml.rs:356-361`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/config/src/config_toml.rs#L356-L361)). Selecting one per invocation needs `-c profile=...`, which is a `-c` override; selecting it in `config.toml` changes the ChatGPT app too.
- "How does each client connect to the proxy?" already rejected profiles for T3 Code, because `--profile` reaches neither `codex app-server` nor `codex exec`.

### Environment variables

- The source defines no variable that selects a model provider or a user config file. The config-location variables are `CODEX_HOME` ([`utils/home-dir/src/lib.rs:6-21`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/utils/home-dir/src/lib.rs#L6-L21)) and `CODEX_SQLITE_HOME` for the state databases ([`state/src/lib.rs:127-128`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/state/src/lib.rs#L127-L128)).
- `CODEX_APP_SERVER_MANAGED_CONFIG_PATH` is read only by `codex app-server`, and only in debug builds ([`app-server/src/main.rs:150-163`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server/src/main.rs#L150-L163)).
- On Linux the legacy managed config is fixed at `/etc/codex/managed_config.toml` ([`config/src/loader/layer_io.rs:222-233`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/config/src/loader/layer_io.rs#L222-L233)). It would apply to every client, including the ChatGPT app.
- `CODEX_OSS_BASE_URL` affects only the built-in local providers ([`model-provider-info/src/lib.rs:740`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/model-provider-info/src/lib.rs#L740)). `CODEX_EXEC_SERVER_URL` is another daemon exclusion.

### Starting the shared server with the override

- The daemon stores only `feature_overrides` and merges them on start ([`app-server-daemon/src/launch.rs:15-26`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/src/launch.rs#L15-L26)). Its command line is `app-server [--remote-control] --listen unix://` plus `-c features.<name>=<bool>` for each saved feature ([`app-server-daemon/src/backend/pid.rs:371-402`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/src/backend/pid.rs#L371-L402)). There is no way to pass `model_provider`.
- If it were possible, it would also send ChatGPT-app remote-control sessions and phone sessions through the proxy, because the shared server serves every client of `~/.codex`.

### A separate `CODEX_HOME` through a wrapper

For example: `CODEX_HOME=~/.codex-cliproxy codex`, where that home's `config.toml` sets `model_provider = "cliproxy"`.

- **Shared server:** kept, but it is a second one. The socket is per `CODEX_HOME` ([`transport/mod.rs:64-70`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-transport/src/transport/mod.rs#L64-L70)). On first start the daemon copies the CLI package into `CODEX_HOME/packages/app-server-daemon` ([`app-server-daemon/README.md:93-97`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/README.md?plain=1#L93-L97)), with its own updater and remote-control settings.
- **Config:** a second `config.toml` to keep in sync with `~/.codex/config.toml` (MCP servers, plugins, features, the `cliproxy` provider). There is no include mechanism, and on Linux no per-home managed layer that could add only `model_provider`.
- **Login:** `auth.json` is per home ([`login/src/auth/storage.rs:154-155`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/login/src/auth/storage.rs#L154-L155)). A copy goes stale: ChatGPT refresh tokens are single use, and a reused one fails with `refresh_token_reused` ([`login/src/auth/util.rs:26-35`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/login/src/auth/util.rs#L26-L35)). A symlink to `~/.codex/auth.json` would work, because the file is rewritten in place with truncate and write, which follows symlinks ([`storage.rs:206-220`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/login/src/auth/storage.rs#L206-L220)). A separate `codex login` is not an option: it is not allowed in this project.
- **History:** sessions, `history.jsonl`, and the state databases (`state_5.sqlite`, `thread_history_1.sqlite`, and others) live in the home. CLI threads would not appear in `~/.codex` resume lists, T3 Code, or the ChatGPT app, and the other way round.
- **Other:** `AGENTS.md`, skills, rules, hooks, plugins, and memories are also per home.

### `--remote unix://` to the existing shared server

- `--remote unix://` connects to the shared server's socket explicitly ([`tui/src/lib.rs:448-458`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/lib.rs#L448-L458)), and an explicit remote target skips the exclusion logic ([`lib.rs:1029-1030`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/lib.rs#L1029-L1030)).
- `thread/start` has a per-thread `model_provider` field ([`app-server-protocol/src/protocol/v2/thread.rs:62-66`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L62-L66)). The TUI fills it when `model_provider` comes from `-c` ([`tui/src/app_server_session/provider_selection.rs:8-24`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app_server_session/provider_selection.rs#L8-L24), [`app_server_session.rs:2051-2053`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app_server_session.rs#L2051-L2053)). So in principle one shared server could serve both direct and proxied threads.
- It does not work today, for three reasons:
  - The shared server reads `CLIPROXY_API_KEY` from its own environment, which is fixed when it starts ("Shared clients use the environment inherited when the daemon started", [`README.md:22-24`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/README.md?plain=1#L22-L24)). The running server does not have the key.
  - `unix://` counts as a remote workspace ([`lib.rs:328-330`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/lib.rs#L328-L330)). The TUI then sends no cwd unless `-C` is given, and drops its sandbox and approval settings ([`app_server_session.rs:2135-2145`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app_server_session.rs#L2135-L2145), [2196-2207](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/app_server_session.rs#L2196-L2207)).
  - An explicit remote target does not start a missing server ([`README.md:27-28`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/README.md?plain=1#L27-L28)).
- Not tested end to end, because a test would send model requests.

## (c) Recommendation

1. **Keep `-c model_provider=cliproxy` for the CLI and accept embedded mode.** The losses are limited to the shared server's own features: run in background, the agents dashboard, and remote control. Every other option costs more, or changes the ChatGPT app or T3 Code.
2. **Add `--no-daemon` to the alias:** `alias codex='codex -c model_provider=cliproxy --no-daemon'`. The behaviour is the same (the TUI is already embedded), but the startup warning goes away, because the warning is printed only when auto-start is on, and `--no-daemon` turns auto-start off ([`startup_orchestration.rs:494-497`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L494-L497), [577-585](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/tui/src/startup_orchestration.rs#L577-L585)). Observed: `codex --profile qwen38 --no-daemon` starts with no warning. `codex -c model_provider=cliproxy --no-daemon resume --help` and `... fork --help` parse. Do the same in the Windows PowerShell function.
3. **Use the bypass for daemon features.** `command codex` (direct, with Dan's own login) still attaches to the shared server, so run in background, the dashboard, and remote control remain available for direct sessions.
4. **Do not use a separate `CODEX_HOME` or `--remote unix://`.** Revisit only if a later Codex release lets `-c model_provider` (or a per-thread provider) pass the daemon compatibility check, or lets the daemon read a client key per thread.

## Gaps

- The losses are read from source. Run in background and the dashboard were not exercised with a proxied session, because that needs a model request.
- The per-thread `model_provider` path on the shared server (`--remote unix://`) is read from source only.
- Windows was not examined on a Windows host. The same TUI code applies, and the daemon supports Windows ([`README.md:13-20`](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/app-server-daemon/README.md?plain=1#L13-L20)).
