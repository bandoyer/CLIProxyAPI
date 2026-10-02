# How often each provider's usage endpoint can be polled

Research for [#13](https://github.com/bandoyer/CLIProxyAPI/issues/13), part of map [#2](https://github.com/bandoyer/CLIProxyAPI/issues/2). Builds on [`research/quota-signals.md`](https://github.com/bandoyer/CLIProxyAPI/blob/research/quota-signals/research/quota-signals.md) (#3).

Question: for each usage endpoint the management panel reads (Claude, Codex, Antigravity, Kimi, xAI, Devin, Meta), is it documented, what rate limits apply, does a call count against the credential's quota, is there evidence that frequent polling gets an account flagged, and what interval do the official clients, the panel, and existing plugins use? Output: a safe polling interval per provider.

Terms follow `GLOSSARY.md`: provider, credential, quota window, reset time.

No endpoint was called for this research. No token or auth file was read. Evidence comes from source code, docs, and public issue reports only.

## Sources

- This repo at `27db8545` (`main`).
- Management panel `router-for-me/Cli-Proxy-API-Management-Center` at `752e0ee7`. Paths prefixed `panel:`.
- Claude Code 2.1.287, the installed native binary (`~/.local/share/mise/installs/claude/2.1.287/claude`), read as text. The JavaScript in it is minified, so function names below are the minified ones.
- `openai/codex` at `d25c114d`. Paths prefixed `codex:`.
- `MoonshotAI/kimi-cli` at `9ab1286b`.
- `steipete/CodexBar` docs at `0ac27e8f` (a widely used multi-provider usage tracker; useful as a second, independent poller).
- Plugins from `router-for-me/CLIProxyAPI-Plugins-Store` `registry.json`, each at the commit listed in the plugin table.
- Public issues on `anthropics/claude-code` and threads on `discuss.ai.google.dev`.

## Short answer

| Provider | Endpoint | Documented | Known limits | Counts against quota | Flag-risk evidence | Interval used elsewhere | Recommended safe interval (per idle credential) |
|---|---|---|---|---|---|---|---|
| Claude | `GET api.anthropic.com/api/oauth/usage` | No | Strict, undocumented. Persistent 429s reported at 30 to 60 s, and within an hour at 10 min. `retry-after` missing or `0`. | No evidence it does | None for polling. ToS forbids automated access with subscription OAuth in general. | Claude Code: on demand only, backs off 5 min to 1 h after 429. Panel: manual. Plugins: 2 min, 5 min, 30 min. CodexBar: 2 to 30 min. | 15 min floor, 30 min default; stagger credentials; back off at least 5 min (up to 1 h) after any 429 |
| Codex | `GET chatgpt.com/backend-api/wham/usage` | No | None reported | No (the official client polls it every 60 s) | None found | Codex TUI: every 60 s, down to 5 s near exhaustion. Panel: manual. Plugins: 5, 15, 30 min. | 5 min; 60 s is the proven ceiling |
| Antigravity | `POST {daily-,}cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary` | No | None reported | No evidence | Mass bans in 2026 for third-party tool use with Antigravity OAuth. No report ties a ban to quota polling alone. | Panel: manual, tries up to 3 hosts. Plugins: 15 min. CodexBar: 2 to 30 min. | 15 min, one host per call |
| Kimi | `GET api.kimi.com/coding/v1/usages` (or `api.kimi.ai`) | No | None reported | No evidence (quota counts model requests) | None found | Kimi CLI: on demand (`/usage`). Panel: manual. CodexBar: 2 to 30 min. | 15 min |
| xAI (free OAuth) | `GET cli-chat-proxy.grok.com/v1/billing` and `?format=credits` | No | None reported | No | None found for billing reads | Grok CLI: on demand (`/usage`). Panel: manual, 2 calls. Plugins: 15 min, plan re-check 24 h. | 15 min for billing; never a chat probe on a timer |
| xAI (paid) | None. Panel sends a real `POST api.x.ai/v1/chat/completions` (`max_tokens: 1`) | n/a | Normal model limits | Yes, spends tokens | Probe traffic looks like real use | Panel: manual. Plugins: 24 h or 60 min opt-in. | Do not poll; use passive 429s. At most one probe per day, opt-in. |
| Devin | `POST server.codeium.com/exa.seat_management_pb.SeatManagementService/GetUserStatus` | No | None reported | No evidence | None found | This repo: at login, then only if `refresh_interval` is set. Panel: once per visible credential per page visit. | 15 min |
| Meta | `POST api.meta.ai/muse-code/key` | No | None reported | Unknown | Unknown | Panel: manual. CodexBar: 2 to 30 min. | Do not poll until we know whether a call rotates the inference key; then hourly at most |

Two rules matter more than the numbers:

1. Prefer passive signals. For Claude and Codex the proxy already receives the same windows in response headers (`anthropic-ratelimit-unified-*`, `x-codex-*`) on every request, at no cost (see #3). Poll only credentials that have had no traffic since their last reading, or whose reading is older than the interval.
2. Poll once shortly after a known reset time, instead of polling often to catch it. The interesting change for routing is the reset, and the reset time is already known from the previous reading.

## Claude: `/api/oauth/usage`

### What Claude Code itself does

In Claude Code 2.1.287 the usage fetcher is `dO()`/`vF()`. It maps three read modes to three URLs:

```js
var Iie={plain:"/api/oauth/usage",
         at_wall:"/api/oauth/usage?at_wall=1&skip_spend=1",
         cedar_ember:"/api/oauth/usage?cedar_ember=1&skip_spend=1"};
```

Callers found in the bundle are all on demand:

- the `/usage` command (aliases `cost`, `stats`), loaded from a separate chunk;
- the extra-usage (overage) check `lWn()`, which calls `dO(e)` when the user hits a limit;
- the at-limit status read (`read:"at_wall"`, `[juniper-tide]`), deduplicated so concurrent callers share one in-flight request;
- the limit-reset offer (`read:"cedar_ember"`).

No timer that polls this endpoint was found. The status line does not need it: since about 2.1.80 Claude Code passes `rate_limits.five_hour` and `rate_limits.seven_day` to status line scripts from response headers ([comment on #31637](https://github.com/anthropics/claude-code/issues/31637)).

The fetcher has its own throttle memory (`Die()`):

```js
var vie=300000, PF=3600000, Aie=3600000;
...
else if(h===429||h===403){S="rate_limited";let T=r.retryAfterMs??0;w=Math.min(T>0?T:vie,PF)}
...
t(`fetchUtilization: ${h} remembered for this bearer; not asking again for ${Math.round(w/1000)}s`)
```

So after a 429 or 403, Claude Code does not ask again with the same bearer for `Retry-After`, or 5 minutes when there is none, capped at 1 hour. After an auth rejection it waits 1 hour.

When signed in to a third-party gateway, Claude Code reads the gateway's `/api/oauth/usage` at most once per 5 minutes and only after newer traffic (`uQn=300000`; `vbt(e,n){if(e>r&&Date.now()-r>uQn)Nlt(n)}`).

### Public reports of rate limiting

- [anthropics/claude-code#30930](https://github.com/anthropics/claude-code/issues/30930) (open, 2026-03-05): persistent `429 rate_limit_error` with `retry-after: 0` for a Max user. "Tested with 30s, 60s, 120s intervals, always 429." Status line tools "typically every 30-60s" trigger it.
- [#31021](https://github.com/anthropics/claude-code/issues/31021) (closed): same 429; Claude Code's own `/usage` also failed while it lasted.
- [#31637](https://github.com/anthropics/claude-code/issues/31637) (closed as inactive): 429 ladder 30 s to 300 s never recovers. One commenter polling every 10 min got "persistent 429 within the first hour"; backing off to 30 min still got 429s regularly.
- Two contradictory, unverified explanations from users:
  - "Rate limits are per-access-token, not per-account. Each new OAuth access token gets a fresh rate limit window (~5 requests before 429)" ([comment on #30930](https://github.com/anthropics/claude-code/issues/30930)). The suggested workaround (refreshing the token) broke Claude Code's own login for other users. Do not copy it: refresh tokens are single use, and the proxy shares them with nothing, but forcing refreshes to dodge a limit is exactly the kind of pattern that looks abusive.
  - The plugin `claude-seat-pacer` states the opposite in code comments: "The endpoint throttles the caller rather than the credential, so a 429 arrives for every seat at once" (`internal/quota/client.go`, `internal/runtime/poller.go`). It spaces reads 400 ms apart for that reason.
- No Anthropic staff response on these issues.

Either way the safe design is the same: few reads per credential, staggered reads across credentials, and a long backoff after a 429.

### Quota and ban risk

- Nothing suggests a usage read counts against the 5-hour or 7-day window. It returns metadata, and Claude Code calls it for `/usage`.
- No report links polling this endpoint to an account action.
- Anthropic's [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) (effective 2025-10-08) forbid accessing the services "through automated or non-human means, whether through a bot, script, or otherwise" except with an API key, and the [Claude Code legal page](https://code.claude.com/docs/en/legal-and-compliance) says subscription OAuth "is designed to support ordinary use of Claude Code and other native Anthropic applications" and that Anthropic "may [enforce] without prior notice". That risk applies to the proxy as a whole. A timer that reads plain `/api/oauth/usage` every few minutes for every credential is a pattern Claude Code itself never produces, so it is one more signal that the traffic is not Claude Code.
- The panel sends `User-Agent: claude-cli/2.1.280 (external, cli)` and `anthropic-beta: oauth-2025-04-20` (`panel:src/utils/quota/constants.ts:111-116`). It also calls `/api/oauth/profile` in the same refresh (`panel:src/features/quota/providers/claude/data.ts:164-176`), so each panel refresh costs two calls per credential.

### Recommendation

- Use the passive `anthropic-ratelimit-unified-*` headers for any credential that served a request since the last reading. They cover the 5-hour and 7-day windows; only the per-model weekly windows need the endpoint (see #3).
- For idle credentials: at most one read per 15 minutes per credential, 30 minutes by default, plus one read about 30 s after a known reset time.
- Stagger reads across credentials (at least 1 s apart) and cap total reads per minute for the whole pool, in case the limit is per caller IP.
- After a 429 or 403: copy Claude Code. Wait `Retry-After`, or 5 minutes, doubling up to 1 hour. Do not refresh the token to escape the limit.
- Skip `/api/oauth/profile` on scheduled reads; it does not change.

## Codex: `/wham/usage`

### What the Codex CLI does

The Codex TUI polls the usage endpoint on a timer while a ChatGPT-authenticated session is open (`codex:codex-rs/tui/src/chatwidget/rate_limits.rs:191-216`):

```rust
/// Poll more often near exhaustion for every ChatGPT account, independently of experiments.
pub(crate) fn rate_limit_refresh_interval(&self) -> Option<std::time::Duration> {
    ...
    let seconds = if used_percent >= 99.0 { 5 }
        else if used_percent >= 90.0 { 15 }
        else if used_percent >= 75.0 { 30 }
        else { 60 };
```

- The main loop sleeps until `last_requested_at + interval` and then calls `refresh_rate_limits(..., RateLimitRefreshOrigin::Periodic)` (`codex:codex-rs/tui/src/app/startup.rs:1197-1319`, `codex:codex-rs/tui/src/app/rate_limit_refresh.rs:36-45`).
- That sends `account/rateLimits/read` to the app server (`codex:codex-rs/tui/src/app/background_requests.rs:815-845`), which calls `BackendClient::get_rate_limits_with_luna_reserve()` or `get_rate_limits_with_reset_credits()` (`codex:codex-rs/app-server/src/request_processors/account_processor.rs:1214-1222`). The backend path is `{base}/wham/usage` (`codex:codex-rs/backend-client/src/client/rate_limit_resets.rs:127`).
- Periodic reads skip the reset-credit details call (`exclude_reset_credit_details: origin == Periodic`).
- Status data older than 15 minutes is shown as stale (`codex:codex-rs/tui/src/status/rate_limits.rs:66`).

So OpenAI's own client reads `/wham/usage` once a minute per open session, and every 5 s when the account is above 99 %. Several open Codex windows on one account multiply that.

### Quota and ban risk

- The official client polls it constantly, so a read does not consume model quota.
- No public report of 429s or account action for `/wham/usage` was found.
- One caveat on data quality, not on safety: `codex-quota-scheduler` notes that "the generic quota endpoint can report 100% remaining on the very window the 429 pointed at while model requests still hit upstream 429 (observed on K12-plan credentials)" (README, "Temporary-exhaustion recovery"). A fresh reading is not proof that a 429 has cleared.

### Recommendation

- Passive first: Codex responses already carry `x-codex-*` headers and `codex.rate_limits` websocket events, which this repo stores (see #3). `codex-quota-scheduler` does the same and polls only accounts "without recent observations".
- Idle credentials: every 5 minutes is conservative. Anything down to 60 s per credential matches the official client. Send the panel's Codex-TUI `User-Agent` and `Chatgpt-Account-Id` as the panel does.

## Antigravity: `retrieveUserQuotaSummary`

- Undocumented internal Google endpoint. The panel tries `daily-cloudcode-pa.googleapis.com`, then `daily-cloudcode-pa.sandbox.googleapis.com`, then `cloudcode-pa.googleapis.com` (`panel:src/utils/quota/constants.ts:68-72`; loop at `panel:src/features/quota/providers/antigravity/data.ts:144`), so one refresh can cost up to three calls.
- The official Antigravity app reads the same data from its local language server (`exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary`, per CodexBar `docs/antigravity.md`). How often the language server calls the cloud endpoint is unknown.
- No rate limit is reported for the endpoint.
- Ban evidence is about third-party use in general. Google suspended many accounts in early 2026 for using Antigravity OAuth in tools such as OpenClaw, then ran an automated unban; Google's statement (2026-03-12): "using third-party tools with your Antigravity login remains against our terms" ([discuss.ai.google.dev/t/131424](https://discuss.ai.google.dev/t/update-on-antigravity-tos-ban/131424)). In that thread one user asked whether a quota monitor tool was also a ban risk and got no answer. No report shows a ban caused by quota reads alone.
- This repo itself calls `loadCodeAssist` for credit hints at most once per 10 minutes per credential, and only when the hint is unknown (`internal/runtime/executor/antigravity_executor.go:44`, `antigravity_executor_credits.go:355-405`).
- Plugins: `quota-pacer`, `credential-priority`, `antigravity-priority`, and `credential-tier-router` default to 15 minutes; `cpa-prometheus` to 5 minutes.

Recommendation: 15 minutes per idle credential, one host per call (remember which host answered), plus a read shortly after a reset time. Because the ban risk comes from the proxy's use of Antigravity at all, keep scheduled reads rare and do not add extra endpoints such as `loadCodeAssist` to every read.

## Kimi: `/coding/v1/usages`

- Undocumented. The official Kimi CLI calls `{base_url}/usages` only from the `/usage` slash command (alias `status`) (`kimi-cli:src/kimi_cli/ui/shell/usage.py:37-95`). No timer.
- CodexBar calls the same endpoint on its normal 2 to 30 minute cadence (CodexBar `docs/kimi.md`). Kimi quotas are counted in model requests (weekly request quota plus "200 requests per 5 hours", per the same doc); a usage read is not a model request, but nothing confirms either way that it is free.
- No rate-limit or ban reports found.

Recommendation: 15 minutes per idle credential.

## xAI

### Free OAuth: `cli-chat-proxy.grok.com/v1/billing`

- Undocumented. The Grok Build CLI reads billing on demand (`/usage`) through its `x.ai/billing` agent method; CodexBar falls back to `cli-chat-proxy.grok.com/v1/billing?format=credits` (CodexBar `docs/grok.md`).
- The panel calls both `?format=credits` (weekly) and plain `/v1/billing` (monthly) per refresh (`panel:src/features/quota/providers/xai/data.ts:216-219`).
- Plugins: `quota-pacer` and `credential-priority` re-check the xAI plan every 24 hours (`xaiPositiveProbeInterval = 24 * time.Hour`) and read remaining quota from live traffic, not from probes.
- No rate-limit or ban reports found.

Recommendation: 15 minutes per idle credential, one call (`?format=credits`) on scheduled reads.

### Paid xAI: no endpoint, the panel spends tokens

- For paid credentials the panel calls `GET api.x.ai/v1/me` and sends a real chat completion: `{"model": ..., "messages":[{"role":"user","content":"ping"}], "max_tokens":1}` (`panel:src/features/quota/providers/xai/data.ts:103-130`).
- It also falls back to this paid probe for free credentials when both billing calls fail (`data.ts:221-235`).
- `grok-inspection` probes with real `/responses` or `/chat/completions` calls, every 60 minutes by default, when scheduled inspection is enabled (`inspection_schedule.go:34`, `probe.go:20`).

Recommendation: never run this on a timer. Paid xAI quota should come from passive 429s. If a health check is needed, make it opt-in and at most daily.

## Devin: `GetUserStatus`

- This repo calls it at login (`internal/auth/devin/record.go:101-119`) and in `DevinExecutor.Refresh` (`internal/runtime/executor/devin_executor.go:143-226`). Devin's `RefreshLead` is nil (`sdk/auth/devin.go:39`), so the refresh loop calls it only when the auth file sets `refresh_interval` (`sdk/cliproxy/auth/conductor_refresh.go:140`, `175-186`). The loop checks every 5 s; a failed refresh waits 5 minutes (`refreshCheckInterval`, `refreshFailureBackoff`, `conductor_refresh.go:26-29`).
- The panel auto-loads Devin quota "once per visible credential per visit. Other providers retain their existing click-to-load behavior. No polling." (`panel:src/features/quota/providers/devin/useDevinQuotaAutoLoad.ts:6-8`).
- How often the official Devin or Windsurf client calls it is unknown. No rate-limit or ban reports found.

Recommendation: 15 minutes per idle credential. In practice this is `refresh_interval_seconds: 900` in a Devin auth file, which already works today (a bare number is read as seconds, `parseDurationValue` in `conductor_refresh.go:216`).

## Meta: `/muse-code/key`

- This is the key-mint endpoint, not a read-only usage endpoint. This repo uses it to exchange the `dca:` token for an inference API key (`internal/auth/meta/meta.go:425-470`, `internal/runtime/executor/meta_executor.go:107-130`). The panel reads `subs_usage` from the same response (`panel:src/features/quota/providers/meta/requests.ts:6`).
- CodexBar does the same and notes "The returned inference key and payment metadata are discarded" (CodexBar `docs/muse.md`). It also notes Meta omits `subs_usage` while the 5-hour window is idle.
- Unknown: whether each call issues a new key, and whether that invalidates the key the proxy is using. If it does, polling could break in-flight credentials.

Recommendation: do not poll until that is known. After that, hourly at most, or only on demand.

## What the panel and plugins do

The panel never polls. Quota loads when the user clicks refresh, and "refresh all" loads the current page (up to 20 credentials, `QUOTA_PAGE_SIZE`) with all calls in parallel and no stagger (`panel:src/features/quota/hooks/useQuotaBatchLoader.ts:52-85`, `panel:src/features/quota/constants.ts:17`). Devin is the one exception (auto-load once per visit).

| Plugin (commit) | Providers polled | Default cadence | Notes |
|---|---|---|---|
| `claude-seat-pacer` (`633bc7e1`) | Claude | 2 min (floor 30 s); max staleness 15 min | 400 ms stagger; one 0.9 s retry on 429, skips if `Retry-After` > 5 s |
| `quota-router` (`17dae28d`) | Claude | Request-triggered, refresh only if cache is at least 5 min old | Not a timer |
| `quota-reset-router` (`62b73e7c`) | Claude, Codex | 5 min (1 min to 1 h), plus shortly after a reset | |
| `cpa-prometheus` (`5d83ab91`) | Claude, Codex, Kimi, Antigravity, xAI, Gemini CLI | 5 min (1 min to 24 h) | Same interval for every provider |
| `cpa-quota-api-extension` (`a1873f6e`) | Several | 30 min cache, request-triggered, 8 concurrent | README: "Claude's OAuth usage endpoint is aggressively rate-limited; the default 30-minute cache reduces calls." |
| `codex-quota-scheduler` (`f27aa969`) | Codex | 30 min; passive observation preferred; sleeps when idle | Probe mode keeps a 30 min minimum |
| `codex-fleet-manager` (`43d3c13a`) | Codex | 30 min | Same design as above |
| `quota-pacer` (`b29e1248`) | Antigravity, Codex, Claude, xAI | 15 min batches, 6 concurrent; xAI plan every 24 h | |
| `credential-priority` (`55637237`) | Antigravity, Codex, xAI | 15 min batches; xAI plan every 24 h | Same code base as `quota-pacer` |
| `antigravity-priority` (`3b4648cb`) | Antigravity | 15 min; 5 min cooldown after 429 | |
| `credential-tier-router` (`49e163a2`) | Codex, Antigravity | 15 min | |
| `codex-token-usage` (`8dffcb71`) | Codex | Off by default; 10 min, real model probe | README warns it "can consume a small amount of tokens and may affect quota" |
| `grok-inspection` (`b97ba3ae`) | xAI | 60 min when scheduled, real model probe | |

CodexBar, outside this ecosystem, uses an adaptive 2 to 30 minute cadence, 5 minutes for older installs (CodexBar `docs/refresh-loop.md`).

## Open questions

1. Is the Claude usage throttle keyed by access token, by account, or by caller IP? The answer decides whether a pool of many Claude credentials behind one proxy can poll each at 15 minutes, or must share one budget.
2. Does `POST /muse-code/key` mint a new key each call, and does that invalidate the previous key?
3. Does a Kimi `/usages` read count as a request against the 5-hour or weekly request quota?
4. How often do the official Antigravity and Devin clients call their quota endpoints? That would give a proven ceiling like Codex's 60 s.
