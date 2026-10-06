# Service scripts

These files run the proxy as a systemd user service, deploy new builds to it, and back up its config.

| File | Purpose |
|---|---|
| `cli-proxy-api-deploy.sh` | Builds `main`, installs the binary, restarts the service, checks health, and rolls back on failure. |
| `cli-proxy-api-deploy_test.sh` | Tests the deploy script against stub commands. It does not touch the live service. |
| `systemd/cli-proxy-api.service` | The user service. It restarts on failure. |
| `systemd/cli-proxy-api-failure.service` | Sends a critical desktop notification each time the service fails. |
| `cli-proxy-api-config-backup.sh` | Backs up `config.yaml` to the `CLIProxyAPI` 1Password vault and records the last successful backup. |
| `cli-proxy-api-config-backup_test.sh` | Tests the backup script against stub `op` and `sleep` commands. It does not call 1Password. |
| `systemd/cli-proxy-api-config-backup.path` | Starts the backup service when `config.yaml` changes. |
| `systemd/cli-proxy-api-config-backup.service` | Runs the backup script. It retries a failed upload every 5 minutes. |

Installed paths:

- Binary: `~/.local/bin/cli-proxy-api` (the previous build is kept as `cli-proxy-api.prev`)
- Config: `~/.config/cli-proxy-api/config.yaml` (mode `600`); this is also the service's working directory
- Unit: `cli-proxy-api.service`

## Install

Run these commands once, from the repository checkout that is on `main`:

```bash
mkdir -p ~/.local/bin ~/.config/cli-proxy-api
ln -sf "$PWD/scripts/cli-proxy-api-deploy.sh" ~/.local/bin/cli-proxy-api-deploy
systemctl --user link "$PWD/scripts/systemd/cli-proxy-api.service" \
  "$PWD/scripts/systemd/cli-proxy-api-failure.service"
```

Put the config at `~/.config/cli-proxy-api/config.yaml` and run `chmod 600` on it. Then do the first deploy, enable the service at boot, and let it run without a login session:

```bash
cli-proxy-api-deploy
systemctl --user enable cli-proxy-api.service
loginctl enable-linger "$USER"
```

## Deploy

Run `cli-proxy-api-deploy` after you merge a fork PR that changes the server. Config edits hot-reload, so they do not need a deploy.

The script does these steps:

1. Refuses to continue if the checkout is not on `main`, then runs `git pull --ff-only`.
2. Builds `./cmd/server` with `main.Version` (`git describe --tags --always --dirty`), `main.Commit` and `main.BuildDate` set by `-ldflags -X`, as the `Dockerfile` does.
3. Copies the current binary to `cli-proxy-api.prev` and installs the new one.
4. Runs `systemctl --user restart cli-proxy-api.service` and polls `GET http://127.0.0.1:8317/healthz` for up to 30 seconds.
5. On success, prints the version line from the startup log.
6. On failure, moves the new binary to `cli-proxy-api.failed`, restores `cli-proxy-api.prev`, restarts, sends a critical `notify-send`, and exits with a non-zero status.

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

## Test

```bash
scripts/cli-proxy-api-deploy_test.sh
scripts/cli-proxy-api-config-backup_test.sh
shellcheck scripts/*.sh
systemd-analyze --user verify scripts/systemd/*.service scripts/systemd/*.path
```

The test builds a throwaway git repository and puts stubs for `go`, `systemctl`, `curl`, `journalctl`, `notify-send` and `sleep` first on `PATH`. `systemd-analyze verify` reports that `~/.local/bin/cli-proxy-api` is missing until the first deploy, and that `~/.local/bin/cli-proxy-api-config-backup` is missing until the backup is installed.
