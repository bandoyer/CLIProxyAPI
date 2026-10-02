# Which thread identifiers and transports do Dan's clients send?

Research for issue #5 (map #2). Terms follow `GLOSSARY.md`: a **thread** is one ongoing conversation in a client, a **credential** is one signed-in subscription, and **affinity** sends every request in a thread to the same credential.

## Short answer

Every client Dan uses sends an explicit thread identifier in a request header. The affinity resolver picks it up at rank 1 (Claude) or rank 3 (Codex), so the prefix matcher (`pickLCP`) and the message-hash fallback never run for these clients.

- Claude Code, run directly or through T3 Code, sends `X-Claude-Code-Session-Id` on every request over HTTP. Subagents keep the same session ID and add `x-claude-code-agent-id`.
- Codex 0.160.0, run as the CLI or as the app server under T3 Code, sends `session-id` and `thread-id` headers. It uses WebSocket by default and falls back to HTTP. Subagents send their own `thread-id`, the root `session-id`, `x-codex-parent-thread-id`, and `x-openai-subagent`.

None of the clients is pointed at the proxy today, and `routing.session-affinity` is off by default. See [Gaps](#gaps).

## Versions examined

| Client | Version | Source |
|---|---|---|
| T3 Code | 0.0.44 (build `451afcb22d93`) | `/usr/lib/t3code/resources/app.asar`, files `apps/server/dist/bin.mjs` ("bin") and `apps/server/dist/claudeHistoryWorker-DPq5P8Kb.mjs` ("worker") |
| Claude Agent SDK (inside T3 Code) | 0.3.276 | worker 89726 |
| Claude Code CLI | 2.1.287 | `~/.local/share/mise/installs/claude/2.1.287/claude` (Bun binary; minified symbol names cited) |
| Codex CLI and app server | 0.160.0 | `openai/codex` tag `rust-v0.160.0` (commit `a956835d`), paths under `codex-rs/`; strings in the installed binary confirm the header names |

T3 Code bundles neither CLI. It runs the `claude` binary from the `binaryPath` setting (both of Dan's Claude instances point to the mise `latest`, which is 2.1.287) and `codex` from `PATH` (0.160.0). So T3 Code sends the same identifiers as the CLIs, with the differences listed below.

## Summary table

"Rank" is the step in `ExtractSessionInfo` (`sdk/cliproxy/session/info.go:83-95`) that returns the identity.

| Client | Provider | Thread identifier | Stable per thread? | Subagent identity | Transport | Resolver rank |
|---|---|---|---|---|---|---|
| Claude Code CLI | Claude | `X-Claude-Code-Session-Id: <session uuid>`; same value in `metadata.user_id` JSON `session_id` | Yes, across turns and compaction. `--resume` keeps the ID; `--fork-session` makes a new one | Same session header plus `x-claude-code-agent-id`; nested agents add `x-claude-code-parent-agent-id` | HTTP (`POST /v1/messages`, SSE) | 1, key `claude:<sid>`; subagent `claude:<sid>:agent:<agentId>` with parent `claude:<sid>` |
| T3 Code, Claude (Agent SDK) | Claude | Same as Claude Code. T3 creates a random UUID with `--session-id`, then `--resume`s it | Yes, across turns and app restarts. Rollback forks a new ID | Same as Claude Code (subagents run inside the CLI) | HTTP | 1 |
| Codex CLI | Codex | `session-id` and `thread-id` headers, equal on a root thread; also in `x-codex-turn-metadata`, `x-client-request-id`, body `prompt_cache_key` and `client_metadata` | Yes, across turns, compaction, and resume. `/new` and forks get a new thread ID | Own `thread-id`, root `session-id`, `x-codex-parent-thread-id`, `x-openai-subagent: collab_spawn` / `review` / ... | WebSocket by default for the built-in `openai` provider; HTTP SSE for custom providers and after fallback | 3, key `codex:<id>`; subagent `codex:<child thread>` with parent `codex:<root>` |
| T3 Code, Codex (app server) | Codex | Same as Codex CLI. T3 stores Codex's thread ID and calls `thread/resume` | Yes, unless `thread/resume` fails and T3 silently starts a new thread | Same as Codex CLI | Same as Codex CLI | 3 |
| T3 Code text generation (titles, branch names, commits, PRs) | Codex (`codex exec --ephemeral`), Dan's setting | New thread ID per call | One request per thread | None | Same as Codex CLI | 3, a new binding per call |

## Claude Code CLI 2.1.287

Headers are built in one place. `pw()` returns `{"x-app": "cli" | "cli-bg", "User-Agent": ..., [Iyt]: q()}`, where `Iyt = "X-Claude-Code-Session-Id"` and `q()` returns the current session ID. The API client factory `tD(...)` spreads `pw()` into `defaultHeaders`, so every request carries the header, whatever `ANTHROPIC_BASE_URL` is. Claude Code protects these headers from user override (`Px = new Set(["x-app", Iyt, "x-claude-code-agent-id", "x-claude-code-parent-agent-id", ...])`).

- **Body.** `V$e()` builds `metadata.user_id` as a JSON string with `device_id`, `account_uuid`, `session_id: q()`, and `parent_session_id` when set. `parent_session_id` comes from `Jk()`, which reads a team context or `cliParentSessionId`. It is not set for ordinary subagents.
- **Subagents.** `tD` adds `"x-claude-code-agent-id": agentId` and `"x-claude-code-parent-agent-id": parentAgentId` when the agent context is not the main agent (`Rp(e)` is `e.agentType === "main"`). The session header stays the same.
- **Gateway hint headers.** `x-claude-code-request-class` (for example `compaction`) and `x-claude-code-agent-type` are only added when `CLAUDE_CODE_GATEWAY_HINT_HEADERS` is set or a first-party feature flag is on (`$cn()`). The resolver does not read them.
- **Stability.** Compaction and side queries run in the same process with the same `q()`. The CLI refuses `--session-id` with `--resume` unless `--fork-session` is also given, which implies that `--resume` keeps the original ID. This was not checked at runtime.
- **Transport.** HTTP through the Anthropic SDK. No WebSocket path to the Messages API was found.

## T3 Code 0.0.44, Claude path

- Uses `@anthropic-ai/claude-agent-sdk` 0.3.276 with `pathToClaudeCodeExecutable: claudeBinaryPath` (bin 126099).
- **First turn.** T3 generates a random UUIDv4 and passes it as `sessionId` (bin 125807-125811). This is not the T3 thread ID. The SDK turns it into `--session-id=<id>` (worker 74582).
- **Later turns.** All turns go into one long-lived `query()` (bin 126349). Model and permission changes use `setModel` and `setPermissionMode`, not a new session (bin 126282-126298).
- **Restarts.** T3 stores the session ID in SQLite as a resume cursor (bin 124383, 124545-124550, 62516) and passes `resume: <id>` after an app restart or the 30-minute idle shutdown (bin 205816).
- **Rollback** calls `forkSession(sessionId, {upToMessageId})` and continues on the new ID (bin 126395-126478). The new session sends no reference to the old one, as far as was checked.
- **Environment.** T3 adds `CLAUDE_CODE_ENTRYPOINT=sdk-ts` and `CLAUDE_AGENT_SDK_VERSION`. It sets no `ANTHROPIC_BASE_URL` and no custom headers; the CLI only sees those if the inherited environment or Claude settings provide them.

## Codex 0.160.0 (CLI and app server)

All paths are under `codex-rs/` at `rust-v0.160.0`.

- **Headers.** `session-id` is the prompt cache key, which is the root thread's ID. `thread-id` is the requesting thread's ID (`codex-api/src/requests/headers.rs:5-14`, `core/src/client.rs:575-597`).
  - `x-client-request-id` is also the thread ID, so it is per thread, not per request (`codex-api/src/endpoint/responses.rs:87-89`).
  - `x-codex-turn-metadata` is a JSON string with `session_id`, `thread_id`, `turn_id`, `window_id`, `request_kind`, `forked_from_thread_id`, `parent_thread_id`, and `subagent_kind` (`core/src/responses_metadata.rs:380-450`).
  - `x-codex-window-id` is `<thread_id>:<n>`, and `n` goes up after each compaction.
  - 0.160.0 sends no `session_id` (underscore) or `conversation_id` header. Go's canonical form of `session-id` is `Session-Id`, which the resolver reads.
- **Body.** `prompt_cache_key` holds the session ID. `client_metadata` repeats the IDs. `store: false`. There is no `metadata` field. `previous_response_id` is sent only on WebSocket follow-ups.
- **Stability.**
  - Compaction runs on the same thread with the same IDs (`core/src/compact.rs:267`).
  - Resume keeps the thread ID and the saved session ID (`core/src/session/session.rs:853-911`).
  - `/new` creates a new UUIDv7 thread.
  - A fork gets a new thread ID and `forked_from_thread_id`. An ephemeral fork keeps the parent's `session-id` (`core/tests/suite/prompt_cache_key.rs:163-218`).
- **Subagents.** `spawn_agent` children and review threads get their own `thread-id`, share the root `session-id`, and send `x-codex-parent-thread-id` and `x-openai-subagent` (`collab_spawn`, `review`, `guardian`, `memory_consolidation`) (`core/src/codex_delegate.rs:69-112`, `core/src/responses_metadata.rs:395-475`, test `prompt_cache_key.rs:126-157`). Auto-generated TUI titles run in a hidden thread with a new ID and no subagent header.
- **Transport.** WebSocket is used when the provider has `supports_websockets` (`core/src/client.rs:1024-1033`).
  - The built-in `openai` provider sets it to `true`, including with an `openai_base_url` override. Custom `model_providers` default to `false`, so they use HTTP (`model-provider-info/src/lib.rs:191-193, 520-558`).
  - The handshake goes to `{base_url}/responses` and carries the same identity headers plus `OpenAI-Beta: responses_websockets=2026-02-06`. Every `response.create` message repeats the IDs in `prompt_cache_key` and `client_metadata` (`core/src/client.rs:1298-1333, 1952-2020`).
  - Codex keeps one connection per thread. It falls back to HTTP for the rest of the session on a 426 response or after its retries run out (`core/src/responses_retry.rs:119-133`).
- **App server.** It uses the same `ModelClient`, so headers and transport are identical. The difference is `originator`, which comes from `clientInfo.name` ("T3 Code" for T3) (`app-server/src/request_processors/initialize_processor.rs:130-169`).
- **T3 Code.**
  - T3 calls `thread/start` once, stores `thread.id`, then calls `thread/resume` and `turn/start` on it (bin 150986-151005, 152030-152048). If resume fails with "not found", it silently calls `thread/start` again, which creates a new ID (bin 150674-150681).
  - T3 text generation uses `codex exec --ephemeral` (bin 127650-127666), which creates a new thread for every call.
- **Dan's config.** `~/.codex/config.toml` sets no `model_provider` or `base_url`, so Codex currently goes straight to `chatgpt.com/backend-api/codex` over WebSocket.

## How the proxy resolves these identifiers today

Affinity only runs when `routing.session-affinity: true`. The default is `false` (`config.example.yaml:141`; `sdk/cliproxy/service_config.go:76-83` wraps the base selector only when it is enabled).

1. `SessionAffinitySelector.Pick` (`sdk/cliproxy/auth/selector.go:973`) calls `extractExplicitSessionIDs` first (`:983`, `:1631`). If that returns an ID, `pickLCP` is skipped. Explicit IDs "are absolute authority".
2. `extractExplicitSessionIDs` calls `ExtractSessionInfo` (`sdk/cliproxy/session/info.go:96`). For Dan's clients:
   - **Rank 1, `X-Claude-Code-Session-Id`** (`info.go:173-223`): ID `claude:<sid>`.
     - The agent ID comes from `X-Claude-Code-Agent-Id`, then `metadata.agent_id`/`subagent_id`, then `metadata.user_id`. With an agent ID, the ID becomes `claude:<sid>:agent:<agentId>`, with parent `claude:<sid>`, or `claude:<sid>:agent:<parentAgentId>` when `X-Claude-Code-Parent-Agent-Id` is set.
   - **Rank 2, `metadata.user_id` JSON `session_id`** (`info.go:225-282`; parser `sdk/cliproxy/session/identity.go:146`): only used if the header is missing. Claude Code always sends the header.
   - **Rank 3, `Session-Id` / `Thread-Id` / `X-Codex-Turn-Metadata`** (`info.go:284-447`). The cases are checked in this order:
     1. Fork: `forked_from_thread_id` in turn metadata or body gives `codex:<thread>` with parent `codex:<forked from>`, and `IsFork`.
     2. Subagent: `X-Openai-Subagent`, `subagent_kind == "thread_spawn"`, `thread-id != session-id`, or a differing `x-codex-parent-thread-id` gives `codex:<thread>` (or `codex:<sid>:agent:<agent_name>` when turn metadata has `agent_name`) with parent `codex:<root>`.
     3. Otherwise `codex:<session-id>`.
   - `X-Client-Request-Id` (step 5 in the code), `prompt_cache_key` (`pck:`), and `conversation_id` would also match Codex, but rank 3 returns first.
3. **Binding key.** The key is `provider::<id>::<model>` (`selector.go:1039`).
   - A subagent ("isSubagentSession": the ID contains `:agent:` or shares the parent's prefix, `selector.go:1561`, `home_session_alias.go:238`) looks up the parent's key as a fallback when `session-affinity-subagents` is true (default). It then binds only its own key (`selector.go:1046-1092`).
   - A non-subagent with a fallback ID binds both keys as aliases.
4. **`pickLCP`** (`selector.go:1114`) fingerprints canonical turns and matches the longest common prefix. It runs only when no explicit ID was found, so it does not apply to Dan's clients.
5. **`home_session_alias.go`** (`homeDispatchSessionIDs`, `:256`) reconciles a primary and fallback ID into one canonical ID for CLIProxyAPIHome dispatch. It uses the same `extractExplicitSessionIDs`. It only matters when Home is in use.
6. **Proxy-side WebSocket.** `GET /v1/responses` upgrades to WebSocket (`internal/api/server_routes.go:78`, `sdk/api/handlers/openai/openai_responses_websocket.go:268`).
   - Each `response.create` runs through the normal executor. Its headers come from the handshake request through the gin context (`sdk/api/handlers/handlers_context.go:155`), so `session-id`/`thread-id` reach the resolver on every message.
   - The handler also pins the chosen credential for the connection (`pinnedAuthByProvider`) and sets a random per-connection execution session ID (`:281`, `:703`). That ID ranks below the explicit headers.
   - A reconnect loses the pin, but the resolver's cache still maps the thread to the same credential.

## Gaps

1. **No client points at the proxy today.** Neither the shell environment nor either Claude settings file sets `ANTHROPIC_BASE_URL`, and `~/.codex/config.toml` has no `base_url` or provider. T3 Code inherits its environment and sets no base URL itself. The routing setup for each client is undecided. For Codex the choice changes the transport: `openai_base_url` keeps WebSocket, while a custom `model_providers` entry uses HTTP unless `supports_websockets = true`.
2. **Affinity is off by default.** No `config.yaml` was found in the main checkout, so it is unknown whether the running proxy sets `routing.session-affinity: true`.
3. **The model is part of the binding key.**
   - `~/.claude/settings.json` sets `CLAUDE_CODE_SUBAGENT_MODEL=opus`. A subagent on a different model than its parent looks up `provider::<parent>::<subagent model>`, which misses the parent's binding, so the subagent may land on another credential.
   - The same applies to Claude side queries on a smaller model, and to a mid-thread model switch in T3 Code (`setModel` or a per-turn Codex model): the thread can move to another credential and pay a cache write.
4. **Thread breaks the proxy cannot see.**
   - T3 Claude rollback forks a new session ID with no parent link that was found.
   - T3 Codex `thread/resume` failure silently starts a new thread.
   - Both look like brand-new threads to the resolver.
5. **One-shot T3 text generation** (`codex exec --ephemeral`, `claude -p`) creates a new thread per call. Each call binds a credential cold. That is harmless for affinity, but it spreads small cache writes across credentials.
6. **Not confirmed at runtime.** No request was captured.
   - That Claude Code 2.1.287 keeps the same session ID on plain `--resume`.
   - That `x-claude-code-parent-agent-id` appears for nested Task agents.
   - Whether Codex 0.160.0 puts `agent_name` in `x-codex-turn-metadata`.
   - That `review` and `collab_spawn` are the values on the wire (the strings are inlined, so they are not visible in the binary).
7. **Doc drift in the proxy.** The priority comment on `ExtractSessionID` (`selector.go:1587-1601`) puts `X-Client-Request-Id` at rank 6, ahead of `session_id` and `prompt_cache_key`. The code checks it after `X-Session-ID`, `X-Session-Affinity`, `X-Slot-Session-Id`, `X-Task-ID`, `X-Conversation-Id`, and `X-Thread-Id` (`info.go:475-644`). This does not affect Dan's clients.
