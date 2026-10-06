# Deploy scripts

These files run the proxy as a systemd user service and deploy new builds to it.

| File | Purpose |
|---|---|
| `cli-proxy-api-deploy.sh` | Builds `main`, installs the binary, restarts the service, checks health, and rolls back on failure. |
| `cli-proxy-api-deploy_test.sh` | Tests the deploy script against stub commands. It does not touch the live service. |
| `systemd/cli-proxy-api.service` | The user service. It restarts on failure. |
| `systemd/cli-proxy-api-failure.service` | Sends a critical desktop notification each time the service fails. |

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

## Test

```bash
scripts/cli-proxy-api-deploy_test.sh
shellcheck scripts/*.sh
systemd-analyze --user verify scripts/systemd/*.service
```

The test builds a throwaway git repository and puts stubs for `go`, `systemctl`, `curl`, `journalctl`, `notify-send` and `sleep` first on `PATH`. `systemd-analyze verify` reports that `~/.local/bin/cli-proxy-api` is missing until the first deploy.
