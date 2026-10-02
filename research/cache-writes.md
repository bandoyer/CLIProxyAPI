# Does the proxy cause avoidable cache writes?

Research for issue #6 (map #2). Terms follow `GLOSSARY.md` on `docs/routing-glossary`: a **thread** is one ongoing conversation, a **credential** is one signed-in subscription, **affinity** keeps a thread on one credential, and a **cache write** is a request that stores a new prompt prefix instead of reading a stored one. Client facts come from `research/client-thread-ids.md` (#5).

Code references are at `origin/main` commit `27db8545`. Nothing was sent to a provider, and no request was captured. Every claim about wire behavior comes from reading code, provider docs, or the installed client binaries.

## Short answer

The proxy does not change the cached prefix from one turn to the next for any of Dan's clients. Nearly all avoidable cache writes come from routing, not from rewriting request bodies:

1. **Credential switches dominate.** Caches belong to one organization, so a thread that moves to another credential rewrites its whole prefix there. With the default config (`session-affinity: false`, `strategy: round-robin`), the proxy can switch credentials on every request. With affinity on, switches happen only on failover, on a model change, and on the thread breaks listed in #5.
2. **Claude Code CLI through the proxy falls back to the 5-minute TTL.** Claude Code picks the 1-hour TTL only when it is signed in to a claude.ai subscription. Pointed at the proxy with a proxy API key, it uses 5 minutes, and the proxy passes that through unchanged. Every idle gap between 5 and 60 minutes then rewrites the whole prefix, where a direct subscriber session would read it. This is not verified at runtime.
3. **T3 Code's Claude traffic is cloaked.** The proxy rewrites the system prompt, adds breakpoints, and upgrades them to 1 hour. These edits are deterministic, so turns stay cacheable. The 1-hour TTL matches what Claude Code does directly for a subscriber. One exception: the injected date block changes at midnight, which rewrites the message history once per day for a thread that is active at that time.
4. **Codex is passed through for caching purposes.** `prompt_cache_key` is forwarded unchanged. The proxy's body edits are stable within a thread.

## Provider rules that matter

**Anthropic** ([prompt caching docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)):

- The prefix is hashed in the order `tools`, `system`, `messages`. "Changes at each level invalidate that level and all subsequent levels." Thinking-parameter changes always invalidate message blocks.
- A request can have at most 4 breakpoints. A read looks back at most 20 blocks from each breakpoint.
- Write prices are 1.25x base input for 5-minute entries and 2x for 1-hour entries. Reads are 0.1x. For Claude Opus 5.5 that is $5, $8, and $0.20 per million tokens against a $4 base. "The cache is refreshed for no additional cost each time the cached content is used."
- A 1-hour entry must come before any 5-minute entry. Billing splits a request into A (read), B−A (1-hour write), and C−B (5-minute write).
- "Caches are isolated between organizations ... Caches are also isolated per workspace within an organization."
- Usage reports `cache_creation_input_tokens`, `cache_read_input_tokens`, and `cache_creation.ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens`.
- The docs do not mention `x-anthropic-billing-header`.

**OpenAI** ([prompt caching docs](https://developers.openai.com/api/docs/guides/prompt-caching)):

- "Cache reuse requires the entire rendered prefix to match", including tools.
- On GPT-5.6 and later, writes cost 1.25x and reads 0.1x (0.05x on GPT-6.1 Sol). Entries live for 30 minutes after their last write or read (`prompt_cache_options.ttl: "30m"`). Routing is automatic, and `prompt_cache_key` is optional. Usage reports `input_tokens_details.cached_tokens` and `cache_write_tokens`.
- On earlier models, writes cost nothing extra and entries usually live for 5 to 10 minutes (up to an hour). There, `prompt_cache_key` helps routing ("about 15 requests per minute" per key).
- "Caches are not shared across organizations."
- How a ChatGPT-subscription Codex plan weighs cache writes and reads against its quota is not documented. Codex 0.160.0 does parse `cache_write_tokens` from the backend (the string appears in the installed binary), so the backend probably reports them.

## How the proxy classifies each client

The Claude executor either passes a request through or cloaks it. `resolveClaudeWirePolicy` (`internal/runtime/executor/claude_executor_cloaking.go:1318-1379`) forces passthrough for a "confirmed" Claude Code request (`:1360`). In auto mode it cloaks everything else that goes to an OAuth credential (`:1358`; `claude_fingerprint_policy.go:96-113` sets `ProfileClaudeCodeCLI` for any OAuth token).

A request is confirmed only when it has all four strong signals and its User-Agent entrypoint is `cli`, `sdk-cli`, or `claude-vscode` (`helps/claude_client_detection.go:66-70, 160`).

| Client | Entrypoint | Proxy path | Cache markers owned by |
|---|---|---|---|
| Claude Code CLI | `cli` | Passthrough | Claude Code |
| T3 Code, Claude (Agent SDK) | `sdk-ts` (T3 sets `CLAUDE_CODE_ENTRYPOINT=sdk-ts`, #5) | **Cloaked** | The proxy (`shouldEnsureCacheControl`, `:1673`) |
| Codex CLI and T3 Codex app server | n/a | Codex executor, HTTP or WebSocket | Implicit (OpenAI) |

The T3 classification follows from the detection code and #5's environment finding. No request was captured to confirm it.

## Causes, one by one

"Direct" means the same client talking to the provider without the proxy. Frequency is per thread.

| # | Cause | Clients | Effect versus direct | How often |
|---|---|---|---|---|
| 1 | Credential switch (round-robin with affinity off, or failover, model change, thread break) | All | Whole prefix written on the new organization | Default config: up to every request. Affinity on: on failover, model or subagent-model change, and T3 rollback or resume failure |
| 2 | Claude Code CLI uses 5m TTL through the proxy | Claude Code CLI | More rewrites after idle gaps; each write is cheaper | Every gap of 5 to 60 minutes between requests |
| 3 | 1h TTL upgrade on cloaked requests | T3 Claude | Every write costs 2x instead of 1.25x. Matches direct subscriber behavior | Every write |
| 4 | Proxy-injected date block in the first user message | T3 Claude | Message history rewritten | Once per day, for a thread active across midnight in the credential's timezone |
| 5 | System prompt relocation and added breakpoints | T3 Claude | Different layout, same stability | Never between turns |
| 6 | Billing header with per-request `cch`, `cc_prev_req`, `cc_prompt_id` | Both Claude paths | None if api.anthropic.com strips the block, as the proxy's docs claim | Never (unverified) |
| 7 | `metadata.user_id` injection or rewrite | Claude | None. Metadata is not part of the prefix | Never |
| 8 | Sensitive-word obfuscation, strict mode, MCP tool aliasing | T3 Claude, when configured | Deterministic, so stable | Only when the config changes mid-thread |
| 9 | Codex body edits (`image_generation` tool, tool schema normalization, `prompt_cache_retention` removal) | Codex | Stable within a thread | Never between turns |
| 10 | Codex `prompt_cache_key` and session headers | Codex | Key forwarded unchanged | Never |

### 1. Credential switch

- **Rule.** Anthropic and OpenAI caches never cross organizations (docs above). Each Claude or ChatGPT subscription is its own organization, so the first request of a thread on a new credential writes everything: tools, system, and all messages.
- **Cost.** For a 100k-token Opus 5.5 prefix, a read costs $0.02 in API-price terms. A rewrite costs $0.50 with 5-minute markers or $0.80 with 1-hour markers, which is 25 to 40 times more. On the next turn the thread reads from the new credential again. Each switch costs one full-prefix write, whatever the thread's length so far. How subscription quota counts this is not documented.
- **When it happens.**
  - Affinity is off by default (`config.example.yaml:141`), and the default strategy is `round-robin` (`:129`). With several credentials for one provider, consecutive turns of a thread can land on different organizations. Each one then writes, or reads only a short anchor such as the tools and system prompt.
  - When a round-robin request returns to a credential, it only reads the old entry if that entry is still within its TTL and within the 20-block lookback of a breakpoint. In an agentic loop, many blocks are added between visits, so the read is usually limited to the stable anchors.
  - With `routing.session-affinity: true`, a thread changes credential only on these events:
    - **Failover.** On 429 or 5xx the proxy tries another credential (`routing.retry`, `config.example.yaml:155-166`), and a cooled or exhausted credential is replaced.
    - **Model change.** The binding key includes the model (`sdk/cliproxy/auth/selector.go:1039`, #5 gap 3). This affects subagents on a different model (`CLAUDE_CODE_SUBAGENT_MODEL=opus`), Claude side queries on a smaller model, and mid-thread model changes in T3.
    - **Thread breaks the proxy cannot see** (#5 gap 4). T3 Claude rollback forks a new session ID whose prefix matches the old session up to the fork point. Direct, it would read that prefix. Through the proxy it can bind to another credential and write the prefix again.
    - **Binding expiry.** `session-affinity-ttl: 1h` matches the longest Anthropic TTL, so an expired binding usually costs nothing extra.
- **Codex over WebSocket.** When the upstream credential must change, the proxy closes the downstream socket and asks for a full HTTP replay (`sdk/api/handlers/openai/openai_responses_websocket.go`, around the `responsesWebsocketHTTPReplayRequiredError` branch). The replayed request carries the full input, so it writes the full prefix on the new organization.

### 2. Claude Code CLI loses the 1h TTL through the proxy

- **What Claude Code does.** The 2.1.287 binary picks the TTL in a function shown below (minified names). `s` is true only for a claude.ai OAuth login with subscription scopes. The function `wl()` returns false when the key source is `ANTHROPIC_API_KEY` or `apiKeyHelper`.

  ```js
  if(!s||g)return{ttl:"5m",reason:"default"} ... return VDe(e,S)?{ttl:"1h",reason:"subscriber"}:{ttl:"5m",reason:"default"}
  ```

  The allowlist for 1 hour is `["repl_main_thread*","sdk","auto_mode","memdir_relevance"]`, and the server-side flag `tengu_prompt_cache_1h_config` can replace it. Overrides that come before this check: `FORCE_PROMPT_CACHING_5M`, `CLAUDE_CODE_PROMPT_CACHE_TTL`, the `promptCacheTtl` setting, and `ENABLE_PROMPT_CACHING_1H`.
- **What the proxy does.** For a confirmed Claude Code request, the proxy does not own the markers. `shouldEnsureCacheControl` returns false (`claude_executor_cloaking.go:1673-1675`), and the 1-hour upgrade only runs when the proxy owns them (`claude_executor_execute.go:214-247`, `claude_executor_stream.go:217-249`). A 5-minute request therefore stays 5 minutes.
- **Effect.** Each write is cheaper (1.25x instead of 2x). However, every pause of more than 5 minutes (reading output, reviewing a diff, a long tool run) loses the whole prefix. Direct, the subscriber session reads it at 0.1x.
- **Break-even.** With prefix P and new tokens per turn Δ, 1 hour costs 0.75Δ more per turn. One 5-to-60-minute gap under 5 minutes costs 1.15P more. For P = 100k and Δ = 3k, 1 hour wins if such a gap happens at least once every ~50 turns. That is almost always true in interactive use.
- **Possible fix.** Set `ENABLE_PROMPT_CACHING_1H=1` or `CLAUDE_CODE_PROMPT_CACHE_TTL=1h` in Claude Code's environment, or let the proxy upgrade passthrough requests on OAuth credentials. Either way, someone has to check that the `extended-cache-ttl-2025-04-11` beta reaches Anthropic.
- **Related, unverified.** Claude Code sends `prompt-caching-scope-2026-01-05` only when `firstPartyOnlyBetas` is true. That is `Im()`, which appears to mean a first-party base URL. Through the proxy Claude Code therefore never marks blocks `scope: "global"`. If global scope lets the static system prompt be shared across organizations, the proxy also loses that, which makes cause 1 slightly more expensive. The public docs do not describe global scope.

### 3. The 1h TTL upgrade (cloaked requests)

- **Where.** `upgradeClaudeCacheControlTTL` (`claude_executor_cloaking.go:1584-1620`) adds `ttl: "1h"` to every marker that has no TTL. It runs only when the proxy owns the markers, the credential uses the CLI profile (every OAuth credential), and the request is not a subagent or probe (`claude_executor_execute.go:238-243`).
- **Subagents and probes.** These are stripped back to 5 minutes unless the caller asked for 1 hour (`:241-242`; `helps/claude_diagnostics.go:542-548`).
- **Cost.** Yes, this raises the price of every write: all written tokens are billed at 2x instead of 1.25x, 60% more per written token.
- **Is it avoidable?** No. Direct, T3's Claude Code would be a subscriber with query source `sdk`, which is on the 1-hour allowlist, so it would also write at 1 hour. Subagents fall back to 5 minutes in both cases. The upgrade therefore matches direct behavior, and per the break-even above it usually saves money in interactive threads.
- **Ordering.** `normalizeCacheControlTTL` (`:1733`) only downgrades a 1-hour marker that follows a 5-minute one. That is required by the ordering rule and does not apply to all-1-hour requests.

### 4. Injected date block (T3 Claude)

- **Where.** In cloaked requests, `injectClaudeCodeCurrentDate` (`claude_executor_cloaking.go:1157-1224`) inserts a `currentDate` system reminder into the first user message on every request. The date is computed from the wall clock in the credential's timezone, then `claude-header-defaults.timezone`, then local time (`:1084-1106`).
- **Effect.** When the date changes, `messages[0]` changes. Every message-level entry is then invalid, so the next request rewrites all history after the tools and system anchor.
- **Duplicate check.** The function removes a matching date reminder from the client, but only one that starts exactly with `# currentDate` (`:1153-1155`). Claude Code 2.1.287 may bundle the date with other context in one reminder. In that case the proxy's block is added next to the client's block. Both are stable within a day.
- **Compared with direct.** Whether native Claude Code changes its own date block mid-session was not checked.
- **Frequency.** Once per day for each thread active within the TTL across midnight. This is small.

### 5. System relocation and breakpoints (T3 Claude)

- **Prefix layout.** Cloaking replaces `system` with `[billing header, "You are Claude Code..." with a marker]`. It moves each caller system block into a mid-conversation system message after the first user turn (current models) or into reminders in the first user message (legacy models) (`checkSystemInstructionsWithSigningModeAt`, `:335-389`). Only the caller blocks' text is kept, so their markers are dropped (`collectForwardedClaudeSystemPromptBlocks`, `:586-609`).
- **Markers after cloaking.** The proxy adds markers on the identity block and on the first real user text. `ensureCacheControl` (`:1541-1551`) fills in a system and a rolling last-message marker if they are missing. `enforceCacheControlLimit(…, 4)` trims to four markers. Typically there are three: identity, first user text, and last message.
- **Stability.** Each step depends only on the request content, so a stable client prompt gives a stable prefix between turns. Claude Code's system prompt now sits in `messages` between the first-user anchor and the rolling marker. A miss on the rolling lookback rewrites it together with the history. Direct, Claude Code's own system marker would have kept it cached. That is a small extra exposure, not a per-turn write.

### 6. Billing header

- **What the proxy sends.** `generateBillingHeader` (`:152-193`) writes `cc_version=<ver>.<fingerprint of the first user text>`, plus `cc_prev_req`, `cc_prompt_id`, and a `cch` hash over the final body (`claude_signing.go:52-58, 205-216`) into `system[0]`. These values change on every request.
- **Why it probably does not matter.** Native Claude Code sends the same per-request values directly, and Claude Code still gets cache hits. That only works if Anthropic leaves this block out of the prefix hash.
- **What the proxy's own docs say.** They state this directly: "api.anthropic.com strips the block itself (0 tokens, no cache impact)" (`config.example.yaml:566-573`). One test still guards against a fingerprint change busting the cache (`claude_cloaked_cache_repro_test.go:19-41`), so the authors were not certain.
- **Through the proxy.** Claude Code CLI omits `cch` because its base URL is not first-party, and the proxy signs it again for OAuth credentials (`claude_signing.go:180-204`). The upstream shape matches a direct request.
- **Verdict.** No extra writes, but the only evidence is the proxy's own claim and native behavior. Anthropic's docs say nothing about this block.

### 7. `metadata.user_id`

`injectFakeUserID` (`claude_executor_cloaking.go:96-130`) runs only for non-OAuth cloaked credentials. OAuth requests get a stable identity through `applyClaudeCLIIdentity` (`claude_fingerprint_policy.go:127-141`). In both cases `metadata` is outside tools, system, and messages, which are the only parts the docs list as cached. No effect.

### 8. Optional cloak settings

- **Sensitive-word obfuscation** inserts zero-width characters in the same places every time (`claude_executor_cloaking.go:1490-1494`; `claude_executor_stream.go:268-271`).
- **Strict mode** drops caller system prompts on every request alike.
- **MCP tool aliasing** derives names from the proxy API key and the tool name (`claude_executor_request.go:1653-1662`).

All three are deterministic. They cause writes only when the configuration or the proxy API key changes during a thread.

### 9 and 10. Codex

- **`prompt_cache_key`.** For Responses-format requests, the proxy copies the client's `prompt_cache_key` into `cache.ID` and writes the same value back (`codex_executor_request.go:94-136`, `codex_websockets_request.go:36-62`). Codex 0.160.0 sets this key to the root session ID (#5), so it is unchanged. On WebSocket the proxy also sends the value as `session_id` and `Conversation_id` (`codex_websockets_request.go:59-61`, `:113-123`). It sends that instead of the client's `session-id`/`thread-id` unless cloaking is disabled. The docs do not say that these headers affect caching.
- **`prompt_cache_retention`.** The proxy deletes it (`codex_websockets_execute.go:60`, `codex_executor_execute.go:62`, `codex_executor_stream.go:65`). Codex 0.160.0 does not send it: in the binary, the string appears only in bundled documentation. This has no effect today.
- **Other body edits.**
  - `ensureImageGenerationTool` (`codex_executor_request.go:365-388`) appends an `image_generation` tool when it is missing. It skips free-plan credentials, Spark models, and lite requests. The result is stable on one credential, but tools can differ between credentials of different plans, which only matters together with cause 1.
  - `NormalizeCodexToolSchemas`, parallel-tool-call normalization, and input-ID sanitizing are deterministic.
  - Reasoning replay (`codex_executor_reasoning.go:72-74`) only runs for Claude-format callers, so it does not affect Codex clients.

## Measuring cache writes on the running proxy

The proxy already records cache tokens for every upstream request:

- **Claude** parses `cache_read_input_tokens` and `cache_creation_input_tokens` (`internal/runtime/executor/helps/usage_helpers.go:1141-1143`).
- **Codex and OpenAI** parse `input_tokens_details.cached_tokens` and `cache_write_tokens` (`usage_helpers.go:1036-1053`).
- **Storage.** Both land in `usage.Detail.CacheReadTokens` and `CacheCreationTokens` (`sdk/cliproxy/usage/manager.go:79-89`), together with `SessionID`, `AuthIndex`, `Model`, and `RequestedAt`.

To read them:

1. In `config.yaml`, set `management.secret-key` and `observability.usage.usage-statistics-enabled: true`. Raise `redis-usage-queue-retention-seconds` to 3600 (the maximum). The queue is filled only when management or Home is enabled and usage statistics are on (`internal/api/server.go:245`; `internal/redisqueue/plugin.go:22-28`).
2. Poll `GET /v8/management/observability/usage/queue?count=500` with `X-Management-Key: <key>` (`internal/api/server_management_v8.go:41`). Each record has `session_id`, `auth_index`, `model`, `timestamp`, and `tokens.{input_tokens, cache_read_tokens, cache_creation_tokens}`.
   - The endpoint pops records, so run only one consumer.
   - With CLIProxyAPIHome enabled, Home also drains the queue (`sdk/cliproxy/service_home.go:364`).
   - The deprecated `/v0/management/usage-queue` route returns the same data.
3. Group records by `session_id`, ordered by time. For each request compute `cache_creation_tokens / (input_tokens + cache_read_tokens + cache_creation_tokens)`.
   - A high ratio on a turn after the first is a write the thread did not need.
   - If `auth_index` changed from the previous request, the cause is a credential switch (cause 1).
   - If the gap since the previous request was longer than 5 minutes on a Claude Code CLI session, the cause is the TTL (cause 2).
   - If the write happened right after midnight on a T3 session, the cause is the date block (cause 4).
   - Any other unexplained write points at a prefix change the proxy makes, which this research did not find.

**Gap.** The proxy does not record Anthropic's 5-minute and 1-hour split (`usage.cache_creation.ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens`), so you cannot see the cost difference between the TTLs from usage records alone. `observability.logs.request-log: true` captures full upstream responses, including that split, but it also logs every prompt.

## Open questions worth a ticket

1. Should Claude Code CLI keep the 1-hour TTL through the proxy? The options are a client env var (`ENABLE_PROMPT_CACHING_1H=1`) or a proxy upgrade for passthrough requests on OAuth credentials. Either needs a check that `extended-cache-ttl-2025-04-11` reaches Anthropic.
2. Record the 5-minute and 1-hour write split in usage records (`parseClaudeUsageNode`), so Dan can price writes.
3. Turn on `routing.session-affinity` and drop the model from the binding key for subagents and side queries (#5 gap 3). This is the largest lever.
4. Verify on the wire that T3 Code's Claude traffic arrives as `sdk-ts` and is cloaked. Then decide whether cloaking a genuine Claude Code binary is wanted, since it moves Claude Code's system prompt out of `system`.
5. Measure whether api.anthropic.com really ignores the billing header for caching: a second identical request should show `cache_read_input_tokens > 0`. Also measure how subscription quotas count cache writes and reads.
6. Should the injected date use a per-thread date rather than the wall clock, so a thread that crosses midnight keeps its cache?
