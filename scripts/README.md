# Service scripts

These files run the proxy as a systemd user service, deploy new builds to it, back up its config, watch it, and report on its routing.

| File | Purpose |
|---|---|
| `cli-proxy-api-deploy.sh` | Builds `main`, installs the binary and the management panel, restarts the service, checks health, and rolls back on failure. |
| `cli-proxy-api-deploy_test.sh` | Tests the deploy script against stub commands. It does not touch the live service. |
| `systemd/cli-proxy-api.service` | The user service. It restarts on failure. |
| `systemd/cli-proxy-api-failure.service` | Sends a critical desktop notification each time the service fails. |
| `cli-proxy-api-config-backup.sh` | Backs up `config.yaml` to the `CLIProxyAPI` 1Password vault and records the last successful backup. |
| `cli-proxy-api-config-backup_test.sh` | Tests the backup script against stub `op` and `sleep` commands. It does not call 1Password. |
| `systemd/cli-proxy-api-config-backup.path` | Starts the backup service when `config.yaml` changes. |
| `systemd/cli-proxy-api-config-backup.service` | Runs the backup script. It retries a failed upload every 5 minutes. |
| `cli-proxy-api-monitor.sh` | Checks the proxy and sends a desktop notification for each new problem. |
| `cli-proxy-api-monitor_test.sh` | Tests the monitor against stub `curl`, `journalctl` and `notify-send` commands. It does not call the live proxy. |
| `systemd/cli-proxy-api-monitor.service` | Runs the monitor once. |
| `systemd/cli-proxy-api-monitor.timer` | Starts the monitor service every 5 minutes. |
| `cli-proxy-api-routing-report.sh` | Prints wasted quota, binding moves and picks for the last N hours of the journal. |
| `cli-proxy-api-routing-report_test.sh` | Tests the report against a stub `journalctl` command. |

Installed paths:

- Binary: `~/.local/bin/cli-proxy-api` (the previous build is kept as `cli-proxy-api.prev`)
- Config: `~/.config/cli-proxy-api/config.yaml` (mode `600`); this is also the service's working directory
- Management panel: `~/.config/cli-proxy-api/static/management.html`, built from the panel fork checkout at `~/Work/Cli-Proxy-API-Management-Center`
- Unit: `cli-proxy-api.service`

## Install

Run these commands once, from the repository checkout that is on `main`:

```bash
mkdir -p ~/.local/bin ~/.config/cli-proxy-api
ln -sf "$PWD/scripts/cli-proxy-api-deploy.sh" ~/.local/bin/cli-proxy-api-deploy
systemctl --user link "$PWD/scripts/systemd/cli-proxy-api.service" \
  "$PWD/scripts/systemd/cli-proxy-api-failure.service"
```

Put the config at `~/.config/cli-proxy-api/config.yaml` and run `chmod 600` on it. In the `management` section, set `disable-auto-update-panel: true`, so that the proxy does not replace the panel that the deploy installs.

The panel is built from a clone of the panel fork, [bandoyer/Cli-Proxy-API-Management-Center](https://github.com/bandoyer/Cli-Proxy-API-Management-Center), on `main`. The build needs `bun` on `PATH`:

```bash
git clone https://github.com/bandoyer/Cli-Proxy-API-Management-Center.git ~/Work/Cli-Proxy-API-Management-Center
mise use -g bun@1.3.14
```

Then do the first deploy, enable the service at boot, and let it run without a login session:

```bash
cli-proxy-api-deploy
systemctl --user enable cli-proxy-api.service
loginctl enable-linger "$USER"
```

## Deploy

Run `cli-proxy-api-deploy` after you merge a fork PR that changes the server or the panel. Config edits hot-reload, so they do not need a deploy.

The script does these steps:

1. Refuses to continue if the checkout is not on `main`, then runs `git pull --ff-only`.
2. Builds `./cmd/server` with `main.Version` (`git describe --tags --always --dirty`), `main.Commit` and `main.BuildDate` set by `-ldflags -X`, as the `Dockerfile` does.
3. Copies the current binary to `cli-proxy-api.prev` and installs the new one.
4. Builds the panel: in the panel checkout, runs `git pull --ff-only`, `bun install --frozen-lockfile` and `bun run build`, then installs `dist/index.html` as `~/.config/cli-proxy-api/static/management.html`. If the checkout is missing or not on `main`, `bun` is missing, or the pull or build fails, the script logs a warning, keeps the installed page, and continues. It also warns if the config does not set `disable-auto-update-panel: true`.
5. Runs `systemctl --user restart cli-proxy-api.service` and polls `GET http://127.0.0.1:8317/healthz` for up to 30 seconds.
6. On success, prints the version line from the startup log.
7. On failure, moves the new binary to `cli-proxy-api.failed`, restores `cli-proxy-api.prev`, restarts, sends a critical `notify-send`, and exits with a non-zero status. The rollback does not restore the previous panel.

To check the running version later:

```bash
journalctl --user -u cli-proxy-api.service -o cat | grep 'CLIProxyAPI Version' | tail -n 1
```

### Overrides

The defaults match the installed paths. The test uses these variables to run in a sandbox.

| Variable | Default |
|---|---|
| `CLI_PROXY_API_REPO` | The checkout that contains the script (symlinks are resolved) |
| `CLI_PROXY_API_BIN` | `~/.local/bin/cli-proxy-api` |
| `CLI_PROXY_API_UNIT` | `cli-proxy-api.service` |
| `CLI_PROXY_API_HEALTH_URL` | `http://127.0.0.1:8317/healthz` |
| `CLI_PROXY_API_HEALTH_ATTEMPTS` | `30` (one per second) |
| `CLI_PROXY_API_PANEL_REPO` | `~/Work/Cli-Proxy-API-Management-Center` |
| `CLI_PROXY_API_STATIC_DIR` | `~/.config/cli-proxy-api/static` (the proxy's static directory for this config) |
| `CLI_PROXY_API_CONFIG` | `~/.config/cli-proxy-api/config.yaml` (only read for the `disable-auto-update-panel` check) |
| `CLI_PROXY_API_BUN` | `bun` |

## Config backup

The backup keeps a copy of `config.yaml` (management key, client keys, plugin settings) as the `config.yaml` document in the `CLIProxyAPI` 1Password vault. Credentials are not backed up, because their refresh tokens rotate.

When `config.yaml` changes, the `.path` unit starts the backup service. The script waits until the file has not changed for 10 seconds, so a burst of panel writes gives one upload. Then it edits the document, or creates it on the first backup. If the content is the same as the last backup, it does not upload. A failed upload is logged to the journal, the last-success record stays as it was, and the service tries again after 5 minutes.

### Install the backup

Do these steps once, after you create the `CLIProxyAPI` vault and a 1Password service account that can only use that vault:

1. Save the service account token with mode `600`. Paste the token, then press Ctrl+D:

   ```bash
   (umask 077 && cat >~/.config/cli-proxy-api/config-backup.token)
   ```

2. Install the script and the units, and start watching the config:

   ```bash
   ln -sf "$PWD/scripts/cli-proxy-api-config-backup.sh" ~/.local/bin/cli-proxy-api-config-backup
   systemctl --user link "$PWD/scripts/systemd/cli-proxy-api-config-backup.service" \
     "$PWD/scripts/systemd/cli-proxy-api-config-backup.path"
   systemctl --user enable --now cli-proxy-api-config-backup.path
   ```

3. Do the first backup, then make sure that it succeeded:

   ```bash
   systemctl --user start cli-proxy-api-config-backup.service
   journalctl --user -u cli-proxy-api-config-backup.service -o cat | tail -n 5
   ```

### Last-success record

After each successful upload, the script writes `~/.local/state/cli-proxy-api/config-backup-last-success` (mode `600`). The file has three `key=value` lines:

```text
time=2026-10-06T12:00:00Z
epoch=1791288000
sha256=<SHA-256 of the config.yaml content that was uploaded>
```

The backup is stale if the file is missing or its `sha256` is not the same as `sha256sum ~/.config/cli-proxy-api/config.yaml`. After an edit, the backup is stale for the debounce period plus the upload time, so a check must allow a few minutes.

### Backup overrides

| Variable | Default |
|---|---|
| `CONFIG_BACKUP_FILE` | `~/.config/cli-proxy-api/config.yaml` |
| `CONFIG_BACKUP_TOKEN_FILE` | `~/.config/cli-proxy-api/config-backup.token` |
| `CONFIG_BACKUP_VAULT` | `CLIProxyAPI` |
| `CONFIG_BACKUP_DOCUMENT` | `config.yaml` |
| `CONFIG_BACKUP_DEBOUNCE_SECONDS` | `10` |
| `CONFIG_BACKUP_RECORD` | `~/.local/state/cli-proxy-api/config-backup-last-success` |

## Monitor

Every 5 minutes, the timer runs the monitor. The monitor sends a desktop notification when one of these problems starts:

| Problem | Notification | Check |
|---|---|---|
| The proxy is down | `CLIProxyAPI is down` (critical) | `GET http://127.0.0.1:8317/healthz` fails. |
| A credential needs a re-login | `CLIProxyAPI credential needs a re-login` | In `GET /v8/management/credentials`, the credential has `status` `error`, or it is disabled with a refresh message (`unauthorized`, `invalid grant`, `token expired`). |
| The config is not backed up | `CLIProxyAPI config not backed up` | `config.yaml` differs from the last-success record of the backup, and the file did not change in the last 5 minutes. |
| Wasted quota | `CLIProxyAPI wasted quota` | The journal has a `quota window reset` line with `kind=ranking` (the longest window) and `share_left` more than `0.2`. |

These rules prevent repeated notifications:

- A quota cooldown (`status_message` `quota exhausted`, or a cooldown with reason `quota` or `credential_quota`) does not notify. A `transient upstream error` does not notify, because it clears by itself.
- A wasted-quota alert fires at most once for each credential, window and reset time. The monitor keeps the alerts that it sent in `~/.local/state/cli-proxy-api/monitor/wasted-quota-alerted`.
- Each other problem notifies once. It notifies again only after it clears and then comes back. The current problems are in `~/.local/state/cli-proxy-api/monitor/problems`.
- If the proxy is down, the monitor does not read the credentials. The credential problems from the last run stay as they were.

The monitor reads the management key from a file with mode `600`. It gives the key to `curl` on standard input, so the key is not in the process arguments. The monitor never logs the key.

### Install the monitor

1. Save the management key with mode `600`. The proxy stores only a hash of `management.secret-key` in `config.yaml`, so use the plaintext key. Paste the key, then press Ctrl+D:

   ```bash
   (umask 077 && cat >~/.config/cli-proxy-api/monitor-management.key)
   ```

2. Install the script and the units, and start the timer:

   ```bash
   ln -sf "$PWD/scripts/cli-proxy-api-monitor.sh" ~/.local/bin/cli-proxy-api-monitor
   systemctl --user link "$PWD/scripts/systemd/cli-proxy-api-monitor.service" \
     "$PWD/scripts/systemd/cli-proxy-api-monitor.timer"
   systemctl --user enable --now cli-proxy-api-monitor.timer
   ```

3. Run the monitor once, then look at its log. If there are no problems, it sends no notification and logs nothing:

   ```bash
   systemctl --user start cli-proxy-api-monitor.service
   journalctl --user -u cli-proxy-api-monitor.service -o cat | tail -n 5
   ```

### Monitor overrides

| Variable | Default |
|---|---|
| `CLI_PROXY_API_HEALTH_URL` | `http://127.0.0.1:8317/healthz` |
| `CLI_PROXY_API_CREDENTIALS_URL` | `http://127.0.0.1:8317/v8/management/credentials` |
| `CLI_PROXY_API_MANAGEMENT_KEY_FILE` | `~/.config/cli-proxy-api/monitor-management.key` |
| `CLI_PROXY_API_UNIT` | `cli-proxy-api.service` |
| `CONFIG_BACKUP_FILE`, `CONFIG_BACKUP_RECORD` | As for the backup |
| `MONITOR_BACKUP_GRACE_SECONDS` | `300` |
| `MONITOR_JOURNAL_HOURS` | `24` (journal hours searched for reset lines) |
| `MONITOR_WASTED_QUOTA_THRESHOLD` | `0.2` |
| `MONITOR_STATE_DIR` | `~/.local/state/cli-proxy-api/monitor` |

## Routing report

Run the report to judge routing after real use. It reads the last N hours (default 24) of the `cli-proxy-api.service` journal:

```bash
ln -sf "$PWD/scripts/cli-proxy-api-routing-report.sh" ~/.local/bin/cli-proxy-api-routing-report
cli-proxy-api-routing-report 12
```

The report has three summaries. A summary with no log lines shows `none`.

- **Wasted quota per credential and window**, from the `quota window reset` lines. `RESETS` is the number of resets. `WASTED` is the total share left at those resets, as a percentage of one window. `LAST` is the share left at the latest reset.
- **Binding moves per thread**, from the `affinity binding ended` lines. Each row is one ended binding: `FROM` is the credential that the thread left, `TO` is the credential of the thread's next pick, and `REASON` is the reason in the log. `?` means that no pick followed yet. A pick that has no ended binding before it is not a move. Examples are a new thread, or a binding that expired after 1 hour idle.
- **Picks per credential**, from the `expiring-first pick` lines, with a count for each reason: `more_urgent`, `no_data`, `binding_kept` and `credit_only`. `OTHER` counts picks with any other reason.

The report prints each binding-end reason as it is in the log, so a new reason value needs no change to the report. The reasons are defined in `sdk/cliproxy/auth/session_affinity_binding_end.go`.

Quota readings are kept only in memory. After a restart, a window logs its reset only after a new reading arrives.

## Test

```bash
scripts/cli-proxy-api-deploy_test.sh
scripts/cli-proxy-api-config-backup_test.sh
scripts/cli-proxy-api-monitor_test.sh
scripts/cli-proxy-api-routing-report_test.sh
shellcheck scripts/*.sh
systemd-analyze --user verify scripts/systemd/*.service scripts/systemd/*.path scripts/systemd/*.timer
```

The tests use a fake `HOME` and put stub commands first on `PATH`: `go`, `bun`, `systemctl`, `curl`, `journalctl`, `notify-send`, `op` and `sleep`, as each script needs. The deploy test also builds throwaway git repositories for the proxy and the panel fork. `systemd-analyze verify` reports that `~/.local/bin/cli-proxy-api` is missing until the first deploy, and that `~/.local/bin/cli-proxy-api-config-backup` and `~/.local/bin/cli-proxy-api-monitor` are missing until they are installed.
