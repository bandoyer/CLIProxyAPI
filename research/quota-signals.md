# Quota signals per provider and credential

Research for [#3](https://github.com/bandoyer/CLIProxyAPI/issues/3), part of map [#2](https://github.com/bandoyer/CLIProxyAPI/issues/2).

Question: for each provider, which quota windows exist per credential, what does the proxy learn about each one (usage percent, remaining amount, reset time), where does each signal come from, and is it available for an idle credential or only after traffic?

Terms follow `GLOSSARY.md`: provider, credential, quota window, reset time.

Sources:

- This repo at commit `27db8545` (`main`). Paths without a prefix are in this repo.
- The management panel `router-for-me/Cli-Proxy-API-Management-Center` at commit `752e0ee7`. Paths prefixed `panel:` are in that repo.
- The official Codex CLI `openai/codex` at commit `d25c114d`, for Codex header names and the usage endpoint.

Upstream quota endpoints and headers used by subscription products are mostly undocumented. The field names below come from code that parses them (this repo, the panel, the Codex CLI), not from provider API references.

## Short answer

- The proxy itself records quota passively. It copies quota headers from each response into `QuotaState.Signals`, but only for Claude and Codex (Devin is filled another way). The values stay raw strings, nothing in routing reads them, and they exist only after the credential has served traffic.
- For every other provider, the proxy learns a reset time only when a request fails with 429 and the error carries a retry hint. That hint becomes the cooldown `NextRecoverAt`.
- The panel's Quota Management page gets its numbers a different way. It calls each provider's own usage endpoint through the proxy's `POST /v0/management/api-call` tool, using the credential's token. These pulls work for idle credentials and return usage percent and reset time for every window. The panel does not read `quota.signals` at all.
- So a complete, idle-safe picture (all windows, percent, reset time) exists today only in the panel's pull path, and only for Claude, Codex, Antigravity, Kimi, xAI, Devin, and Meta. The proxy does not call those endpoints for routing.

## How the proxy and the panel learn about quota

There are four separate mechanisms. None feeds the others.

### 1. Passive header observation in the proxy (`QuotaState.Signals`)

`QuotaState` (`sdk/cliproxy/auth/types.go:175`) has two halves:

- Cooldown fields: `Exceeded`, `Reason`, `NextRecoverAt`, `BackoffLevel`. The scheduler sets these after a failure, usually a 429.
- Observation fields: `ObservedAt` and `Signals map[string]string`. These hold a raw copy of quota headers from the most recent upstream response.

Observation rules (`sdk/cliproxy/auth/quota_signals.go`):

- Only `claude`, `codex`, and `devin` support observation (`ProviderSupportsQuotaObservation`, line 17). For every other provider the snapshot is cleared.
- After each request, the conductor copies matching response headers into `Signals` for the credential and for the per-model state (`sdk/cliproxy/auth/conductor_cooldown.go:987-991`).
- The header filter (`isQuotaSignalHeaderForProvider`, line 170) keeps:
  - Claude: `Retry-After` and every `anthropic-ratelimit-unified-*` header.
  - Codex: `Retry-After`, `x-ratelimit-*` (the code comment says this is expected to be inert), and `x-codex-*` headers whose names contain `-used-percent`, `-window-minutes`, `-reset-after-seconds`, `-reset-at`, `-limit-reached`, `-allowed`, `-limit-name`, or `-over-secondary-limit-percent`, plus `x-codex-plan-type`, `x-codex-active-limit`, and `x-codex-credits-*`.
  - Devin: no headers match. Devin signals are written by the executor instead (see the Devin section).
- Codex websocket sessions send quota as `codex.rate_limits` events, not headers. `ParseCodexQuotaEventHeaders` (`internal/runtime/executor/helps/codex_quota.go:28`) converts each event into the same `X-Codex-*` header names, so websocket and HTTP traffic land in the same snapshot.
- A response with quota headers replaces the whole snapshot. A response with none (5xx, transport error) leaves the old snapshot in place (`quota_signals.go:26-55`). Values are stored as raw strings; nothing parses them into numbers or times.
- The snapshot is capped at 64 headers and 512 bytes per value (`quota_signals.go:11-12`).
- `GET /v0/management/auth-files` returns the snapshot on each entry as `quota.observed_at`, `quota.signals`, and `model_quotas` (`internal/api/handlers/management/auth_files.go:673-676`, `quotaObservationPayload` at line 808).

Nothing in routing reads `Signals`. A search for `.Signals` outside `quota_signals.go` and `types.go` finds only the management payload and the Devin executor, and the comment on `ObserveResponseHeadersForProvider` says it never touches cooldown or scheduling fields. Today the signals are display data only.

Because the snapshot comes from responses, it exists only after the credential has served traffic (Devin is the exception). It can also be stale: a credential idle for six hours still shows its last five-hour utilization, not zero.

### 2. Reset hints from 429 errors (`QuotaState.NextRecoverAt`)

When a request fails with 429, the conductor marks the credential `Exceeded` and sets `NextRecoverAt` (`applyAuthFailureState`, `sdk/cliproxy/auth/conductor_cooldown.go:2269-2290`):

- If the executor supplied a retry hint, the cooldown is that hint, with a 10 second floor (`minQuotaCooldownFloor`, `sdk/cliproxy/auth/conductor_refresh.go:39`).
- If not, the cooldown is exponential backoff from 1 second up to 30 minutes (`conductor_refresh.go:37-38`, `nextQuotaCooldown` at `conductor_cooldown.go:2331`).

So `NextRecoverAt` is a real reset time only when the provider puts one in its error, and the proxy learns it only once the credential is already exhausted. Each provider's retry hint is listed in its section below.

### 3. Pull from provider usage endpoints (management panel)

The panel's Quota Management page has one adapter per provider (`panel:src/features/quota/providers/index.ts`): Claude, Codex, Antigravity, Kimi, xAI, Devin, and Meta. Each adapter sends `POST /v0/management/api-call` with the credential's `auth_index`, the provider's usage URL, and an `Authorization: Bearer $TOKEN$` header. The proxy substitutes the credential's access token and forwards the request through the credential's proxy settings (`internal/api/handlers/management/api_tools.go:49-103`). The panel then parses the upstream JSON itself.

These pulls are on demand (page load or a user click), not on a schedule, and the results stay in the browser. They do not need prior traffic, so they work for idle credentials. The URLs are listed in `panel:src/utils/quota/constants.ts`.

The panel does not read `quota.signals` from the auth-files response. The Devin normalizer says so explicitly: "Normalize the live Connect-RPC response, never auth-file metadata or quota.signals" (`panel:src/services/api/devinQuota.ts:33`).

Note: `AGENTS.md` marks `/v0/management` as deprecated, but the panel depends on `api-call` for every quota pull.

### 4. Plugin quota providers and declarative probes (proxy)

The proxy also has a normalized quota API: `GET /v0/management/quota/providers` and `POST /v0/management/quota/fetch` (`internal/api/server_management.go:84-86`, `internal/api/handlers/management/plugin_quota.go:43-128`). `FetchCredentialQuota` asks a plugin `QuotaProvider` first. If no plugin handles the provider, it runs a declarative probe from the auth file's `quota_probe` metadata (`executeQuotaProbe`, line 377). The response has `groups[].buckets[]`, and each bucket has `window`, `remainingFraction`, and `resetTime` (`sdk/pluginapi/types.go:1657-1663`).

No plugin or probe ships for any provider in this repo, and the panel's quota page does not call this API. It is the closest existing shape to "one quota window with a remaining fraction and a reset time", but it is empty unless someone configures it.

## Per provider

Column meanings:

- Proxy signal: what the proxy itself stores, and from where.
- Panel signal: what the Quota Management page pulls.
- Reset time: whether the source gives a reset time for that window.
- Idle: whether the signal is available for a credential that has not served traffic.

### Claude (OAuth subscription)

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| 5-hour | Headers `anthropic-ratelimit-unified-5h-utilization`, `-5h-reset`, `-5h-status` in `Signals` | `GET https://api.anthropic.com/api/oauth/usage`, `five_hour.utilization`, `five_hour.resets_at` | Yes, both | Proxy: no. Panel: yes |
| 7-day (all models) | Headers `anthropic-ratelimit-unified-7d-*` | `seven_day.utilization`, `seven_day.resets_at` | Yes, both | Proxy: no. Panel: yes |
| 7-day per model (Opus, Sonnet), OAuth apps, Cowork | Not as separate headers | `seven_day_opus`, `seven_day_sonnet`, `seven_day_oauth_apps`, `seven_day_cowork` | Panel: yes | Panel: yes |
| 7-day Fable | Headers `anthropic-ratelimit-unified-7d_oi-*` | `iguana_necktie`, or a `limits[]` entry scoped to the model (`percent`, `resets_at`) | Yes | Proxy: no. Panel: yes |
| Overage / extra usage (monthly credit) | Headers `anthropic-ratelimit-unified-overage-status`, `-overage-disabled-reason` | `extra_usage.monthly_limit`, `used_credits`, `utilization` | No | Panel: yes |

- On 429, `ParseClaudeRateLimitReset` (`internal/runtime/executor/helps/claude_ratelimit.go:107`) takes the latest reset among the rejected windows (`Retry-After`, `-5h-reset`, `-7d-reset`, `-7d_oi-reset`, `-unified-reset`) plus 1 to 30 seconds of random fuzz. An overage-only or Fable-only rejection is kept model-scoped and does not cool the whole credential (`ClaudeHeadersIndicateUnifiedRateLimitRejection`, line 24).
- Reset headers are parsed as Unix seconds, RFC 3339, or HTTP dates (`parseUnixOrTimestamp`, line 259).
- Panel: `panel:src/utils/quota/constants.ts:110-131` (URL, `anthropic-beta: oauth-2025-04-20`, window keys), parsing in `panel:src/features/quota/providers/claude/data.ts:55-100`, fetch at line 157. The window length is not in the payload; the panel infers it from the key name.

### Codex (ChatGPT OAuth)

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| 5-hour (primary) | Headers `x-codex-primary-used-percent`, `-window-minutes`, `-reset-after-seconds` or `-reset-at`; websocket `codex.rate_limits` converted to the same names | `GET https://chatgpt.com/backend-api/wham/usage`, `rate_limit.primary_window.used_percent`, `limit_window_seconds`, `reset_after_seconds`, `reset_at` | Yes, both | Proxy: no. Panel: yes |
| Weekly (secondary); monthly on some team plans | Headers `x-codex-secondary-*` | `rate_limit.secondary_window.*`; the panel labels it monthly when `limit_window_seconds` is about one month | Yes, both | Proxy: no. Panel: yes |
| Additional per-model limits (for example a Spark model) | HTTP headers `x-codex-<short-name>-primary-*` / `-secondary-*`; websocket `additional_rate_limits` stored as `X-Codex-Additional-<name>-*` | `additional_rate_limits[]` with the same window fields | Yes | Proxy: no. Panel: yes |
| Code review | Websocket `code_review_rate_limits` stored as `X-Codex-Code-Review-*` | `code_review_rate_limit.*` | Yes | Proxy: no. Panel: yes |
| Credits balance | Headers `x-codex-credits-balance`, `-has-credits`, `-unlimited` | `credits.balance`, `credits.unlimited` | No | Proxy: no. Panel: yes |

- The window length is explicit (`window_minutes` in headers, `limit_window_seconds` in the usage payload), so 5-hour and weekly are identified by duration, not position.
- On 429, `parseCodexRetryAfter` (`internal/runtime/executor/codex_executor_terminal.go:424`) reads `resets_at` or `resets_in_seconds` from an error of type `usage_limit_reached`. Per-minute `rate_limit_exceeded` errors carry no hint and get backoff.
- The official Codex CLI parses the same header family (`openai/codex: codex-rs/codex-api/src/rate_limits.rs`) and reads usage from `{base}/wham/usage` (`openai/codex: codex-rs/backend-client/src/client/rate_limit_resets.rs:127`).
- Panel: URL at `panel:src/utils/quota/constants.ts:134`, `Chatgpt-Account-Id` header added in `panel:src/features/quota/providers/codex/data.ts:311`, parsing at lines 88-200, fetch at line 416.

### Antigravity (Google Cloud Code OAuth)

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| 5-hour and weekly buckets, grouped by model family | None (provider not observed) | `POST https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary` (daily and sandbox hosts tried first) with `{"project": "<project_id>"}`; `groups[].buckets[]` with `window` (`5h`, `weekly`), `remainingFraction`, `resetTime` | Panel: yes | Panel: yes |
| Google One AI credits | Internal hint from `POST .../v1internal:loadCodeAssist`, `paidTier.availableCredits[GOOGLE_ONE_AI].creditAmount` and `minimumCreditAmountForUsage`; not exposed to management | Separate subscription call | No | Proxy: refreshed during requests or token refresh, at most every 10 minutes, only when `quota-exceeded.antigravity-credits` is on |

- On 429, `decideAntigravity429` (`internal/runtime/executor/antigravity_executor_credits.go:226`) reads the delay from `google.rpc.RetryInfo.retryDelay`, then `ErrorInfo.metadata.quotaResetDelay`, then "after Ns" in the message (`helps.ParseRetryDelay`, `internal/runtime/executor/helps/json_retry_helpers.go:27`). Reason `QUOTA_EXHAUSTED`, or a delay of 5 minutes or more, counts as full exhaustion.
- Panel: URLs at `panel:src/utils/quota/constants.ts:68-72`, fetch at `panel:src/features/quota/providers/antigravity/data.ts:119`, bucket parsing at `panel:src/utils/quota/builders.ts:67`.

### Kimi (Kimi Code OAuth)

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| Rate-limit windows (`limits[]`, each with `window.duration` and `timeUnit`) | None | `GET https://api.kimi.com/coding/v1/usages` (or `api.kimi.ai`), `limits[].detail.used`/`limit`/`remaining`, `reset_at`/`reset_time`/`reset_in` | Panel: yes | Panel: yes |
| Weekly summary | None | `usage.used`, `usage.limit`, reset fields | Panel: yes | Panel: yes |
| Monthly total | None | `usages.limit_month_total.used_ratio`, `reset_time` | Panel: yes | Panel: yes |

- The Kimi executor attaches no retry hint to errors, so a 429 always gets exponential backoff (`internal/runtime/executor/kimi_executor.go`).
- Kimi reports counts (used and limit), not only a percentage.
- Panel: `panel:src/features/quota/providers/kimi/data.ts:21`, parsing at `panel:src/utils/quota/builders.ts:329`. When the duration is missing, the panel guesses the window length from the label text.

### xAI (Grok CLI OAuth, or API key)

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| Weekly credit period (subscription) | None | `GET https://cli-chat-proxy.grok.com/v1/billing?format=credits`, `config.creditUsagePercent`, `currentPeriod.type/start/end`, `productUsage[].usagePercent` | Panel: period end | Panel: yes |
| Monthly billing period | None | `GET https://cli-chat-proxy.grok.com/v1/billing`, `monthlyLimit`, `used`, `onDemandCap`, `onDemandUsed`, `prepaidBalance`, billing period start and end | Panel: period end | Panel: yes |
| Free tier (rolling 24 hours) | 429 with `free-usage-exhausted` sets a fixed 24-hour cooldown | None | No (fixed guess) | No |
| API key credentials | None | Health check only: `GET https://api.x.ai/v1/me` plus a one-token `POST /v1/chat/completions` | No | Panel: yes, but it spends a request |

- `xaiStatusErr` (`internal/runtime/executor/xai_executor_response.go:1097`) adds the 24-hour hint for free-tier exhaustion. Other 429s get backoff.
- Panel: URLs at `panel:src/utils/quota/constants.ts:155-161`, period parsing at `panel:src/utils/quota/builders.ts:470`. The reset time is the period end, and the panel derives the window length from start and end.

### Devin

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| Daily | `Signals["daily_quota_remaining_percent"]`, `Signals["daily_quota_reset_at"]` from Connect-RPC `GetUserStatus` | `POST https://server.codeium.com/exa.seat_management_pb.SeatManagementService/GetUserStatus`, `dailyQuotaRemainingPercent`, `dailyQuotaResetAtUnix` | Yes, both | Proxy: at login and on refresh. Panel: yes |
| Weekly | `weekly_quota_remaining_percent`, `weekly_quota_reset_at` | `weeklyQuotaRemainingPercent`, `weeklyQuotaResetAtUnix` | Yes, both | Same as daily |

- Devin is the only provider where the proxy pulls quota itself. `internal/auth/devin/record.go:101-119` writes the signals at login, and `DevinExecutor.Refresh` (`internal/runtime/executor/devin_executor.go:143-226`) rewrites them. Devin's `RefreshLead` is nil (`sdk/auth/devin.go:39`), so automatic refresh runs only if the auth file sets `refresh_interval`. Otherwise the snapshot is from login time.
- Devin reports remaining percent; Claude and Codex report used percent.
- Panel: `panel:src/features/quota/providers/devin/requests.ts:80`, normalizer at `panel:src/services/api/devinQuota.ts:47-51`.

### Meta (Muse Code)

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| Short window (`subs_usage.window`, with `window_duration_mins`) | None | `GET https://api.meta.ai/muse-code/key`, `used_percent`, `resets_at`, `window_duration_mins` | Panel: yes | Panel: yes, but `subs_usage` may be absent before the first request |
| Weekly (`subs_usage.weekly`) | None | `used_percent`, `resets_at` | Panel: yes | Same |

- On 429 (or 404), `parseMetaRetryAfter` (`internal/runtime/executor/meta_executor.go:345`) reads `error.resets_at`.
- Panel: `panel:src/features/quota/providers/meta/requests.ts:6`, parsing at `panel:src/services/api/metaQuota.ts:23-75`.

### Gemini (API key), Vertex, AI Studio, OpenAI-compatible

| Quota window | Proxy signal | Panel signal | Reset time | Idle |
|---|---|---|---|---|
| Per-minute and per-day request and token limits | None | None | No | No |

- These executors attach no retry hint, except OpenAI-compatible providers, which pass a standard `Retry-After` header through and use a one-minute fallback for TPM errors (`openAICompatRetryAfter`, `internal/runtime/executor/openai_compat_executor.go:1097`). All others get exponential backoff on 429.
- Gemini in this repo is API-key only (`internal/runtime/executor/gemini_executor.go`). There is no Gemini CLI OAuth executor, so there is no per-credential subscription quota to read.

## Gaps and observations

1. The proxy has no idle-safe quota signal for routing. Accurate percent and reset time for every window exist only in the panel's on-demand pulls, which run in the browser and never reach the scheduler.
2. `Signals` holds raw header strings with no typed model. Turning it into "window, used fraction, reset time" would need per-provider parsing. Claude headers use utilization from 0 to 1, Codex uses percent from 0 to 100, and Devin uses remaining percent.
3. The proxy observes only the 5-hour and 7-day Claude windows (plus Fable and overage). The per-model 7-day windows (Opus, Sonnet) are visible only through `/api/oauth/usage`.
4. Antigravity, Kimi, xAI, and Meta have no passive signal at all. For these, the proxy knows a credential is exhausted only after a 429, and for Kimi and xAI subscriptions it does not get a reset time even then.
5. The plugin `QuotaProvider` / `quota_probe` API already defines a normalized bucket (`window`, `remainingFraction`, `resetTime`) but has no built-in implementation. A built-in quota provider that runs the same pulls the panel runs would give the proxy idle-safe data in one shape.
6. Pulling usage endpoints from the proxy on a schedule has a cost. The panel sends client-like `User-Agent` headers to these private endpoints, and polling many credentials could be rate limited or flagged. How often each endpoint can be polled is unknown.
