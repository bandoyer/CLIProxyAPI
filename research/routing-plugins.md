# What the existing quota-aware routing plugins do

Research for [#4](https://github.com/bandoyer/CLIProxyAPI/issues/4) on map [#2](https://github.com/bandoyer/CLIProxyAPI/issues/2).
Read on 2026-10-01. Terms follow `GLOSSARY.md` (credential, quota window, reset time, affinity, expiring-first routing).

Sources:

- The plugin store registry at <https://raw.githubusercontent.com/router-for-me/CLIProxyAPI-Plugins-Store/main/registry.json> (101 entries on the read date).
- Each plugin's source at the commit named in its section. Plugin code was read only; nothing was written to any plugin repository.
- This fork's host code at commit [`27db8545`](https://github.com/bandoyer/CLIProxyAPI/tree/27db85455dddfd078e212baa54008ad9c07126b8). Host links below point at that commit; `CPA:` is short for `https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/`.

## Short answer

- **No plugin gives expiring-first routing for every provider.** Each one covers one to four providers, because each fetches its own quota data from provider-specific endpoints or headers.
- **Expiring-first routing exists in pieces.** For Codex: codex-quota-scheduler, codex-fleet-manager, smart-load-balancer and credential-priority. For Claude: quota-reset-router, and claude-seat-pacer (pacing). For Antigravity: antigravity-priority and credential-priority. quota-pacer spreads load by "quota left minus time left" across Antigravity, Codex, Claude and xAI.
- **Only claude-seat-pacer combines a reset-time rule with per-thread affinity.** Every other scheduler plugin that makes the pick itself bypasses CPA's affinity. Most then send every thread to one top credential.
- **A new scheduler plugin could do expiring-first routing for any provider that reports reset times.** It would have to fetch quota data itself, rebuild affinity itself, and hold the single scheduler slot. The host gives it no quota data and no access to CPA's affinity bindings. Details are in [What a scheduler plugin can and cannot control](#what-a-scheduler-plugin-can-and-cannot-control).

## How plugins can change credential selection

Plugins in the registry use three mechanisms.

1. **Scheduler capability (`scheduler.pick`).** The host calls the plugin before its own selector. The plugin returns one candidate's ID, asks for the built-in round-robin or fill-first, declines (`Handled=false`), or rejects the request. See the host flow below.
2. **Rewriting `priority`, `weight` or `disabled` on credentials.** A background job writes the credential's auth file, through `host.auth.save` or directly on disk. CPA's normal selector then reads the new values. This keeps CPA's affinity, because a bound thread outranks priority ([CPA: sdk/cliproxy/auth/selector.go#L965-L968](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/selector.go#L965-L968)). Disabling a credential does move its threads, because disabled credentials are dropped before selection ([CPA: sdk/cliproxy/auth/conductor_selection.go#L1755](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L1755)). Priority -1 is only the lowest tier, not "never use": CPA still picks it when every higher tier is cooling down.
3. **ModelRouter.** Picks a model or provider before credential selection. The model routers in the registry (priority-auto-router, model-router, model-fallback-router) never set a credential ID, so they do not choose credentials.

## Summary table

"Affinity" here means: does a thread stay on one credential while it is usable?

| Plugin | Providers | Mechanism | Rule | Uses reset times? | Works with affinity? | Licence | Last commit |
| --- | --- | --- | --- | --- | --- | --- | --- |
| credential-priority | Antigravity, Codex, xAI | Writes priority and disabled | Paid first, then earliest reset; long window resetting within 24h boosted | Yes (polled) | Yes, CPA's, until it disables a credential | MIT | 2026-08-07 |
| codex-quota-scheduler | Codex | Scheduler, all tiers | Highest "pressure" = long-window % left / hours to reset | Yes (polled, headers, stream) | No: bypasses CPA's, no own | MIT | 2026-09-23 |
| quota-router | Claude, listed models only | Scheduler | Skip credentials over a 7-day usage cutoff, then highest priority, lowest ID | Only to unblock | No for listed models (all go to one credential) | MIT | 2026-08-09 |
| credential-tier-router | Codex, Antigravity | Writes priority | Tiers by % left; optional "reset within 24h goes first" mode | Yes, coarse (polled) | Yes, CPA's | MIT | 2026-08-24 |
| priority-auto-router | Any, by model alias | ModelRouter | Ordered model fallback | No | Yes, CPA's (it does not pick credentials) | MIT | 2026-06-23 |
| codex-429-autoban | Codex (but delegates for all) | Scheduler | Drop credentials banned after a 429; else built-in round-robin | Only to unban | No: breaks CPA's for every provider | MIT | 2026-07-18 |
| quota-activation | Codex, Antigravity | Scheduler for its own pings only | Sends one ping per quota cycle to start the next window | Yes, to time pings | Yes for user traffic; takes the scheduler slot | MIT | 2026-08-16 |
| quota-pacer | Antigravity, Codex, Claude, xAI | Writes priority and weight | Weighted round-robin by (% left minus % time left) | Yes (polled, headers) | Yes, CPA's | MIT | 2026-09-13 |
| antigravity-priority | Antigravity | Writes priority and disabled | Boost credentials that cannot spend their weekly quota before reset | Yes (polled) | Yes, CPA's, until it disables a credential | MIT | 2026-09-28 |
| smart-load-balancer | Codex (reset data); any (accepts) | Scheduler, all tiers | Earliest weekly or monthly reset, then most used | Yes (headers) | Own, per client API key, not per thread | None in repo (registry says MIT) | 2026-09-22 |
| quota-reset-router | Claude, Codex | Scheduler | Earliest weekly reset | Yes (polled) | No: bypasses CPA's, no own | MIT | 2026-09-24 |
| claude-seat-pacer | Claude | Scheduler + interceptor | Seat furthest behind its weekly pace | Yes (polled, headers) | Yes, own per-thread binding | MIT | 2026-10-02 |
| codex-fleet-manager | Codex | Scheduler | Same pressure rule as codex-quota-scheduler, top tier only | Yes (polled) | No | MIT | 2026-09-28 |
| codex-token-usage | Codex OAuth, xAI | Scheduler | Declines unless bans exist; then filter and copy CPA strategy | Only to unban | Partly: own binding from session headers | MIT | 2026-08-12 |
| ark-429-autoban | Volcano Engine ARK keys | Scheduler | Declines unless bans exist; then filter and copy CPA strategy | Only to unban | Partly: own binding from headers and metadata | MIT | 2026-08-20 |
| opencode-go-pool | OpenCode Go | Scheduler | Declines while healthy; round-robin when degraded | Only to unblock | Yes while healthy; own 24h map when degraded | MIT | 2026-07-15 |
| opencode-go-quota | OpenCode | Scheduler | Round-robin when `auto_pool` is on | Only to unblock | No | MIT | 2026-08-20 |
| provider-rate-limiter | Any | Scheduler | First candidate under its per-minute limit | No | No | None in repo | 2026-08-22 |
| key-account-bind | Any | Scheduler | Per client key allowed set, then its own round-robin, weighted or fill-first | No | No | MIT | 2026-08-31 |
| key-provider-access | Any | Scheduler | Per client allow and deny lists, then round-robin | No | No | MIT | 2026-09-11 |
| cpa-account-concurrency | Any | Scheduler | Fewest in-flight requests | No | Honours `pinned_auth_id` only | None in repo | 2026-09-24 |

model-router and model-fallback-router choose models only and are covered under [Model routers](#model-routers).

Registry entries not covered, because they do not change credential selection: dashboards and usage trackers (for example quota-center, cpa-quota-estimator, cpa-prometheus), billing and key-management plugins, codex-auto-reset (redeems reset credits), codex-5h-quota-warmer (starts 5-hour windows on a schedule), and interceptors such as codex-switch-safe.

## The seven plugins named in the ticket

### credential-priority (Cody292)

- Repo: <https://github.com/Cody292/credential-priority> at [`55637237`](https://github.com/Cody292/credential-priority/tree/55637237ff034bf6e3ad9d6fc8be199316e056d0) (2026-08-07), release v1.1.6, 86 commits. The registry still lists 1.1.0. MIT.
- Providers: Antigravity, Codex, xAI ([README.en.md#L25](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/README.en.md#L25)).
- Mechanism: no scheduler. It registers `management_api` and `usage_plugin` ([runtime.go#L181-L184](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/runtime/runtime.go#L181-L184)). A 15-minute job, off by default (`auto_apply: false`), writes `priority` and `disabled` into the auth files ([apply.go#L163-L175](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/apply/apply.go#L163-L175)). The README says writes go through `host.auth.save`, but the code writes the files directly ([client.go#L213-L229](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/host/client.go#L213-L229)), so it needs the file store.
- Rule: per provider, among credentials with quota left: paid plans first, then earliest reset time, then auth index ([planner.go#L624-L653](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/priority/planner.go#L624-L653)). Priorities count down from 100 ([L376-L394](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/priority/planner.go#L376-L394)). A credential whose long window resets within 24 hours is boosted ([boost.go](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/priority/boost.go#L8-L48), [production_runner.go#L559](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/runtime/production_runner.go#L559)). For paid Codex the sort key is the 5-hour reset while that window exists ([parse_window_select.go#L33-L50](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/provider/codex/parse_window_select.go#L33-L50)). Exhausted credentials get priority -1, and some are also disabled.
- Reset times: yes. Polled from Codex `wham/usage`, Antigravity `retrieveUserQuotaSummary`, and xAI 429s seen through `usage.handle`. It ranks on the reset timestamp; quota left only decides eligibility.
- Affinity: CPA's affinity still applies. Demoting a bound credential leaves its threads in place; disabling it moves them.
- Risks: it re-enables any credential with quota, including ones disabled by hand ([planner.go#L385-L386](https://github.com/Cody292/credential-priority/blob/55637237ff034bf6e3ad9d6fc8be199316e056d0/internal/priority/planner.go#L385-L386)). Tests are git-ignored, so none are published.

### codex-quota-scheduler (JefferyZhang2019)

- Repo: <https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler> at [`f27aa969`](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/tree/f27aa969ec726a5cc01bdbfcd588cae49306f452) (2026-09-23), release v0.3.2, about 244 commits, 47 test files. MIT. The most actively maintained plugin in this set.
- Providers: Codex only ([scheduler.go#L56](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/blob/f27aa969ec726a5cc01bdbfcd588cae49306f452/scheduler.go#L56)).
- Mechanism: scheduler with `SchedulerAcrossPriorities`, plus usage, lifecycle, quota provider, stream observer, and a ModelRouter and executor for an optional retry chain ([config.go#L569-L592](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/blob/f27aa969ec726a5cc01bdbfcd588cae49306f452/config.go#L569-L592)). An opt-in mode also sets `disabled` through `host.auth.save`.
- Rule: classify accounts (excluded, fresh data, unknown data), then sort by CPA priority, plugin priority, optional monthly-first, then **quota pressure**: long-window percent left divided by hours until that window resets, with a 30-minute floor. Ties go to earliest reset, then most left, then ID ([selection.go#L137-L166](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/blob/f27aa969ec726a5cc01bdbfcd588cae49306f452/selection.go#L137-L166), [scheduler_snapshot.go#L233-L258](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/blob/f27aa969ec726a5cc01bdbfcd588cae49306f452/scheduler_snapshot.go#L233-L258)). Pressure is a per-hour "use it or lose it" rate, which is close to expiring-first routing. If no account qualifies, it delegates to built-in fill-first ([scheduler.go#L101-L121](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/blob/f27aa969ec726a5cc01bdbfcd588cae49306f452/scheduler.go#L101-L121)).
- Reset times: yes, 5-hour and weekly or monthly. From polling `wham/usage`, `codex.rate_limits` stream frames, `X-Codex-*` headers, and `usage_limit_reached` errors.
- Affinity: none of its own; it never reads session headers. Returning an ID bypasses CPA's affinity. Every request goes to the top account, and a thread can move when another account's pressure overtakes it.

### quota-router (Smarty-Pants-Inc)

- Repo: <https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router> at [`17dae28d`](https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router/tree/17dae28d4e37a0dd156a1c2a5456cb3a67048602) (2026-08-09), release v0.5.0, 7 commits, has tests. MIT.
- Providers: Claude OAuth, and only for models in `protected-models` (default `claude-fable-5` at a 50% cutoff) ([README.md#L3-L5](https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router/blob/17dae28d4e37a0dd156a1c2a5456cb3a67048602/README.md#L3-L5)).
- Mechanism: scheduler, top tier only. No poller and no auth writes.
- Rule: skip credentials at or over the 7-day usage cutoff, then pick highest priority, then lowest ID ([scheduler.go#L56-L91](https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router/blob/17dae28d4e37a0dd156a1c2a5456cb3a67048602/scheduler.go#L56-L91)). If all are blocked, the request fails with `quota_router_exhausted`. Other models are declined and go to CPA's selector.
- Reset times: only to keep a blocked credential blocked until its 7-day reset ([cache.go#L41-L47](https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router/blob/17dae28d4e37a0dd156a1c2a5456cb3a67048602/cache.go#L41-L47)). Data from `api.anthropic.com/api/oauth/usage` with its own HTTP client, which skips per-credential proxies ([usage.go#L30-L38](https://github.com/Smarty-Pants-Inc/cpa-plugin-quota-router/blob/17dae28d4e37a0dd156a1c2a5456cb3a67048602/usage.go#L30-L38)).
- Affinity: bypassed for listed models. Threads stay together only because every request goes to the same credential.

### credential-tier-router (William-zgx)

- Repo: <https://github.com/William-zgx/cpa-plugin-credential-tier-router> at [`49e163a2`](https://github.com/William-zgx/cpa-plugin-credential-tier-router/tree/49e163a2cfa190809a52a5926774187a9bad800d) (2026-08-24), v0.1.2, 3 commits, 7 tests. MIT.
- Providers: Codex, Antigravity ([config.go#L37-L47](https://github.com/William-zgx/cpa-plugin-credential-tier-router/blob/49e163a2cfa190809a52a5926774187a9bad800d/config.go#L37-L47)).
- Mechanism: no scheduler, despite the registry tag ([runtime.go#L189](https://github.com/William-zgx/cpa-plugin-credential-tier-router/blob/49e163a2cfa190809a52a5926774187a9bad800d/runtime.go#L189)). A job (off by default) rewrites each auth file's `priority` through `host.auth.save` ([runtime.go#L377-L413](https://github.com/William-zgx/cpa-plugin-credential-tier-router/blob/49e163a2cfa190809a52a5926774187a9bad800d/runtime.go#L377-L413)).
- Rule: tiers Primary 400, Regular 300, Backup 200, Paused -1 ([types.go#L23-L36](https://github.com/William-zgx/cpa-plugin-credential-tier-router/blob/49e163a2cfa190809a52a5926774187a9bad800d/types.go#L23-L36)). Modes: `quota_bands` (by percent left), `balanced`, `reset_soon` (Primary if the reset is within 24 hours and quota is left), `manual` ([runtime.go#L338-L375](https://github.com/William-zgx/cpa-plugin-credential-tier-router/blob/49e163a2cfa190809a52a5926774187a9bad800d/runtime.go#L338-L375)). CPA round-robins inside a tier.
- Reset times: yes, polled through `host.http.do`. `reset_soon` is a coarse yes-or-no version of expiring-first.
- Affinity: CPA's still applies.
- Risk: it reads and saves the whole auth document, which could overwrite a token CPA refreshed in between.

### priority-auto-router (Apparux)

- Repo: <https://github.com/Apparux/cpa-plugin-priority-auto-router> at [`ca1ff68e`](https://github.com/Apparux/cpa-plugin-priority-auto-router/tree/ca1ff68e0234d37941f089995a37ab8e032521ae) (2026-06-23), v0.1.2, 7 commits. MIT. No updates since June.
- Mechanism: ModelRouter plus executor. It calls `host.model.execute` with only a model name, never a credential or provider ([host_model.go#L63-L81](https://github.com/Apparux/cpa-plugin-priority-auto-router/blob/ca1ff68e0234d37941f089995a37ab8e032521ae/host_model.go#L63-L81)).
- Rule: try model candidates in priority order and fall back on eligible errors ([config.go#L199-L221](https://github.com/Apparux/cpa-plugin-priority-auto-router/blob/ca1ff68e0234d37941f089995a37ab8e032521ae/config.go#L199-L221), [executor.go#L41-L61](https://github.com/Apparux/cpa-plugin-priority-auto-router/blob/ca1ff68e0234d37941f089995a37ab8e032521ae/executor.go#L41-L61)).
- Reset times: no. Affinity: CPA's applies inside each call. **It does not choose credentials.**

### codex-429-autoban (ysxk)

- Repo: <https://github.com/ysxk/codex-429-autoban> at [`7a65cba3`](https://github.com/ysxk/codex-429-autoban/tree/7a65cba35658301d944f978b15c644a6719c975a) (2026-07-18), v0.2.2, 12 commits, no tests. MIT.
- Mechanism: usage plugin plus scheduler. Bans are in memory only and are lost on restart. It does not write auth files, despite "auto-disables" in its description.
- Rule: drop Codex credentials whose recorded reset time has not passed. If nothing is banned, delegate to built-in round-robin. If something is banned, pick the highest-priority remaining candidate. If all are banned, decline ([main.go#L401-L461](https://github.com/ysxk/codex-429-autoban/blob/7a65cba35658301d944f978b15c644a6719c975a/main.go#L401-L461)).
- Reset times: only to lift bans, from `x-codex-*` headers on the 429 ([main.go#L355-L397](https://github.com/ysxk/codex-429-autoban/blob/7a65cba35658301d944f978b15c644a6719c975a/main.go#L355-L397)).
- Affinity: **breaks it for every provider.** Delegating to the built-in round-robin skips CPA's affinity selector and the configured strategy, and it does this for non-Codex requests too.
- CPA already cools a credential down after a 429, so most of its value overlaps the host.

### quota-activation (Cody292)

- Repo: <https://github.com/Cody292/quota-activation> at [`ee5c6e13`](https://github.com/Cody292/quota-activation/tree/ee5c6e134e6897125c5a76bd273e480490ee0b7e) (2026-08-16), v0.0.7, 33 commits. MIT.
- Purpose: it does not route user traffic. Once per quota cycle it sends a small request to start a Codex or Antigravity credential's next window.
- Mechanism: registers the scheduler capability ([runtime.go#L194](https://github.com/Cody292/quota-activation/blob/ee5c6e134e6897125c5a76bd273e480490ee0b7e/internal/runtime/runtime.go#L194)) but handles only requests that carry its own one-time `X-Quota-Activation-Nonce` header, and declines the rest ([scheduler.go#L61-L80](https://github.com/Cody292/quota-activation/blob/ee5c6e134e6897125c5a76bd273e480490ee0b7e/internal/scheduler/scheduler.go#L61-L80)). Its fallback transport raises the target's priority to 1000 or more, sends the ping, then restores it ([priority_boost.go#L83-L130](https://github.com/Cody292/quota-activation/blob/ee5c6e134e6897125c5a76bd273e480490ee0b7e/internal/activator/priority_boost.go#L83-L130)).
- Affinity: user traffic falls through to CPA's selector. During a boost, unbound threads go to the boosted credential. **Because it holds the scheduler slot, no other scheduler plugin can run beside it.**

## Other plugins that change credential selection

### Closest to expiring-first routing

**smart-load-balancer (nitansde)** at [`3b59b600`](https://github.com/nitansde/smart-load-balancer/tree/3b59b60030efba586e244d12119e8d58a0d67c98) (2026-09-22). No LICENSE file; the registry and README say MIT.

- Scheduler with all tiers. Ranks by earliest weekly or monthly reset (resets within an hour count as a tie), then most used percent ([balancer.go#L273-L420](https://github.com/nitansde/smart-load-balancer/blob/3b59b60030efba586e244d12119e8d58a0d67c98/balancer/balancer.go#L273-L420)).
- Reset data only for Codex, from the `X-Codex-*` headers.
- Stickiness is keyed on a hash of the client's API key, not the thread ([balancer.go#L112-L122](https://github.com/nitansde/smart-load-balancer/blob/3b59b60030efba586e244d12119e8d58a0d67c98/balancer/balancer.go#L112-L122)), so all of one client's threads share a credential.
- Asking for all tiers lets backup-tier Codex credentials win ([main.go#L626-L634](https://github.com/nitansde/smart-load-balancer/blob/3b59b60030efba586e244d12119e8d58a0d67c98/main.go#L626-L634)).

**quota-reset-router (WebDevCaptain)** at [`62b73e7c`](https://github.com/WebDevCaptain/quota-reset-router/tree/62b73e7c66b40bcef7b3eb2879f99d727290a740) (2026-09-24). MIT, 16 commits.

- Claude and Codex OAuth. Picks highest priority, then earliest weekly `resets_at` ([engine.go#L221-L261](https://github.com/WebDevCaptain/quota-reset-router/blob/62b73e7c66b40bcef7b3eb2879f99d727290a740/engine.go#L221-L261)). Defaults to `shadow` mode, which records but does not route.
- Polls the Anthropic and ChatGPT usage endpoints with its own HTTP client.
- No affinity; its README says CPA's affinity is bypassed ([README.md#L108](https://github.com/WebDevCaptain/quota-reset-router/blob/62b73e7c66b40bcef7b3eb2879f99d727290a740/README.md#L108)).
- Bugs: a credential whose weekly window has not started (`resets_at: null`) is never routed ([quota.go#L60-L69](https://github.com/WebDevCaptain/quota-reset-router/blob/62b73e7c66b40bcef7b3eb2879f99d727290a740/quota.go#L60-L69)). A nil dereference on some Codex payloads can stop the plugin ([quota.go#L105](https://github.com/WebDevCaptain/quota-reset-router/blob/62b73e7c66b40bcef7b3eb2879f99d727290a740/quota.go#L105)).

**claude-seat-pacer (yuya-iwabuchi)** at [`633bc7e1`](https://github.com/yuya-iwabuchi/cpa-plugin-claude-seat-pacer/tree/633bc7e15992878b229be9f40929bd9d14c04e52) (2026-10-02 UTC). MIT, about 236 commits, 344 test functions. Tested on CPA 8.0.4.

- Claude only. Scheduler plus a request interceptor and usage plugin.
- Rule: for each window, target = min(1.10 x elapsed fraction, 1); the seat furthest below target wins. Weights are 1.0 for weekly, 0.5 for model-family windows, 0 for 5-hour ([pace.go#L27-L138](https://github.com/yuya-iwabuchi/cpa-plugin-claude-seat-pacer/blob/633bc7e15992878b229be9f40929bd9d14c04e52/internal/pace/pace.go#L27-L138)). This weighs quota left against time left. It is pacing, not strict expiring-first.
- Data: polls `oauth/usage` through `host.http.do` and reads `anthropic-ratelimit-unified-*` headers.
- **Affinity: its own per-thread binding.** The interceptor sees the request body before selection, derives a thread key (Claude Code headers, session headers, `metadata.user_id`, then a prompt hash), and passes it to its scheduler in `X-Pacer-*` headers ([intercept.go#L25-L85](https://github.com/yuya-iwabuchi/cpa-plugin-claude-seat-pacer/blob/633bc7e15992878b229be9f40929bd9d14c04e52/internal/runtime/intercept.go#L25-L85), [identity.go#L80-L111](https://github.com/yuya-iwabuchi/cpa-plugin-claude-seat-pacer/blob/633bc7e15992878b229be9f40929bd9d14c04e52/internal/session/identity.go#L80-L111)). A thread stays on its seat until refusal or failure; bindings expire after 1 hour idle ([scheduler.go#L189-L263](https://github.com/yuya-iwabuchi/cpa-plugin-claude-seat-pacer/blob/633bc7e15992878b229be9f40929bd9d14c04e52/internal/runtime/scheduler.go#L189-L263)). This interceptor-to-scheduler handoff is the only working way found for a scheduler plugin to see body-derived thread identity.

**quota-pacer (xg1990)** at [`b29e1248`](https://github.com/xg1990/quota-pacer/tree/b29e12480eb7fbf080064e342d3f62a216ce2d7e) (2026-09-13). MIT, 145 commits. It is a continuation of credential-priority and carries its full history ([README.md#L180](https://github.com/xg1990/quota-pacer/blob/b29e12480eb7fbf080064e342d3f62a216ce2d7e/README.md#L180)).

- Antigravity, Codex, Claude, xAI. Writes `priority` (100 for usable credentials) and `weight` for CPA's weighted round-robin.
- Weight = round(1000 x normalized headroom), where headroom is the worst window's (percent left minus percent of time left) ([planner.go#L253-L330](https://github.com/xg1990/quota-pacer/blob/b29e12480eb7fbf080064e342d3f62a216ce2d7e/internal/priority/planner.go#L253-L330), [L557-L617](https://github.com/xg1990/quota-pacer/blob/b29e12480eb7fbf080064e342d3f62a216ce2d7e/internal/priority/planner.go#L557-L617)). Credentials ahead of schedule get more new threads.
- Keeps CPA's affinity ([README.md#L42](https://github.com/xg1990/quota-pacer/blob/b29e12480eb7fbf080064e342d3f62a216ce2d7e/README.md#L42)). Its Claude probe sends a real 1-token Haiku request, which may start a 5-hour window ([claude/probe.go#L69-L77](https://github.com/xg1990/quota-pacer/blob/b29e12480eb7fbf080064e342d3f62a216ce2d7e/internal/provider/claude/probe.go#L69-L77)). It rewrites every healthy credential's file on each run.

**antigravity-priority (ygq-future)** at [`3b4648cb`](https://github.com/ygq-future/antigravity-priority/tree/3b4648cb478a23674524a548ff9871fa9561a7be) (2026-09-28). MIT, about 77 commits.

- Antigravity only. Writes `priority` and `disabled`.
- Boosts a credential that cannot spend its weekly quota before reset at the learned burn rate ([boost.go#L22-L36](https://github.com/ygq-future/antigravity-priority/blob/3b4648cb478a23674524a548ff9871fa9561a7be/internal/priority/boost.go#L22-L36)). Others are ordered by a score of weekly pace, weekly left, 5-hour left and 5-hour reset closeness ([urgency.go#L46-L60](https://github.com/ygq-future/antigravity-priority/blob/3b4648cb478a23674524a548ff9871fa9561a7be/internal/priority/urgency.go#L46-L60)).
- Keeps CPA's affinity until it disables a credential.

**codex-fleet-manager (doer-ee)** at [`43d3c13a`](https://github.com/doer-ee/cpa-plugin-codex-fleet-manager/tree/43d3c13ac5c3fe234e84bc95c539356bb846de93) (2026-09-28). MIT. Derived from codex-quota-scheduler ([README.md#L21-L26](https://github.com/doer-ee/cpa-plugin-codex-fleet-manager/blob/43d3c13ac5c3fe234e84bc95c539356bb846de93/README.md#L21-L26)); same pressure rule, top tier only, no affinity.

### Ban and filter plugins

- **codex-token-usage (zhumengling)** at [`8dffcb71`](https://github.com/zhumengling/codex-token-usage/tree/8dffcb71ce8d99ce1a45fcf39d1dcb63879e44b6) (2026-08-12). MIT. Declines while nothing is banned or blocked, so CPA's affinity stays in control ([main.go#L4485-L4489](https://github.com/zhumengling/codex-token-usage/blob/8dffcb71ce8d99ce1a45fcf39d1dcb63879e44b6/main.go#L4485-L4489)). Otherwise it filters and picks with its own binding, keyed only on `X-Session-ID`, `Session-Id` or `X-Client-Request-Id` headers ([scheduler_affinity.go#L28-L53](https://github.com/zhumengling/codex-token-usage/blob/8dffcb71ce8d99ce1a45fcf39d1dcb63879e44b6/scheduler_affinity.go#L28-L53)). Threads can move when bans start or end.
- **ark-429-autoban (wyx1818)** at [`db9548d9`](https://github.com/wyx1818/ark-429-autoban/tree/db9548d9f3029c36863e98d79511aa921e5679e3) (2026-08-20). MIT. Same pattern for Volcano Engine ARK keys. Its own binding reads session headers and `execution_session_id` or `derived_session_id` from metadata ([affinity.go#L18-L52](https://github.com/wyx1818/ark-429-autoban/blob/db9548d9f3029c36863e98d79511aa921e5679e3/cmd/ark-429-autoban/affinity.go#L18-L52)). Its README states that body session IDs are invisible to it ([README.md#L154-L156](https://github.com/wyx1818/ark-429-autoban/blob/db9548d9f3029c36863e98d79511aa921e5679e3/README.md#L154-L156)).
- **opencode-go-pool (hrz6976)**, <https://github.com/hrz6976/cpa-plugin-opencode-go-pool> (2026-07-15, MIT). Declines while every account is healthy. When degraded, round-robins healthy accounts with its own 24-hour sticky map. Tracks 5-hour, weekly and monthly windows only to unblock at 97% usage.
- **opencode-go-quota (duu261)**, <https://github.com/duu261/opencode-go-pool> (2026-08-20, MIT). Round-robin when `auto_pool` is on; reset times from 429 bodies only to unblock.
- **provider-rate-limiter (lsmallice)**, <https://github.com/lsmallice/cliproxyapi-provider-rate-limiter-plugin> (2026-08-22, no LICENSE file). First candidate under its per-minute limit; never delegates.
- **key-account-bind (FFatTiger)**, <https://github.com/FFatTiger/cpa-key-account-bind> (2026-08-31, MIT). Restricts each client key to bound credentials, then its own round-robin, weighted or fill-first.
- **key-provider-access (GLGDLY)**, <https://github.com/GLGDLY/key-provider-access> (2026-09-11, MIT). Allow and deny lists per client, then round-robin. Its interceptor still enforces the lists when another scheduler wins the slot.
- **cpa-account-concurrency (tsunheimat)**, <https://github.com/tsunheimat/cpa-courrency-plugin> (2026-09-24, no LICENSE file). Fewest in-flight requests; returns 503 when all are busy. Honours `pinned_auth_id`; also looks for session hints this host never sets.

The last six were summarized from a source read whose line-level citations were not kept; the repository links are the source.

### Model routers

model-router (markhuangai, [`7cbaba25`](https://github.com/markhuangai/cpa-plugin-model-router/tree/7cbaba2561143123242204be4081016931949549), 2026-09-23, MIT) and model-fallback-router (thebtf, [`d54ddc7c`](https://github.com/thebtf/cpa-model-fallback-router/tree/d54ddc7c0f064079717813492b55af81d5f91625), 2026-08-03, MIT) map aliases to models with priority, weighted or fallback order. Their host calls carry no credential ID ([model-router execute.go#L82-L91](https://github.com/markhuangai/cpa-plugin-model-router/blob/7cbaba2561143123242204be4081016931949549/execute.go#L82-L91), [model-fallback-router host_model.go#L67-L76](https://github.com/thebtf/cpa-model-fallback-router/blob/d54ddc7c0f064079717813492b55af81d5f91625/host_model.go#L67-L76)). They do not use reset times and leave CPA's affinity in place.

## What a scheduler plugin can and cannot control

### How the host calls it

For each pick (and each retry), `pickNextLegacy` builds the candidate list, then:

1. Drops credentials for another provider, disabled ones, ones already tried in this request, and ones that do not serve the model ([CPA: conductor_selection.go#L1754-L1771](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L1754-L1771)).
2. Drops credentials in cooldown and keeps only the highest priority tier, unless the plugin set `SchedulerAcrossPriorities` ([CPA: conductor_selection.go#L620-L651](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L620-L651)).
3. Calls the plugin ([CPA: conductor_selection.go#L1784](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L1784)). The response is applied in [`pickViaPluginScheduler`](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L950-L993):
   - an ID from the list: that credential is used;
   - `DelegateBuiltin`: the built-in round-robin or fill-first scheduler runs over the host's own pool, which has no affinity ([L922-L948](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L922-L948));
   - `Reject` or an error: the request fails;
   - `Handled=false` or an invalid answer: the configured selector runs, including `SessionAffinitySelector` ([L1788-L1797](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L1788-L1797)).

Mixed-provider routes follow the same pattern ([L2118](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L2118)).

### What it controls

- **Which usable credential serves the request**, among the candidates above, for any provider.
- **Whether to defer**: decline and let CPA's selector (and its affinity) decide, or hand off to built-in round-robin or fill-first.
- **Whether to fail the request** with its own error code.
- **Its own state.** A plugin can run background pollers, call provider usage APIs with a credential through `host.http.do`, observe responses through usage, stream and WebSocket hooks, and read or save auth files through `host.auth.*`.

Inputs it receives ([CPA: sdk/pluginapi/types.go#L486-L525](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/pluginapi/types.go#L486-L525)): provider or providers, model, stream flag, request headers, request metadata, and per candidate the ID, provider, priority, status and non-secret attributes.

### What it cannot control or see

1. **No host quota data.** CPA records quota headers per credential for Claude, Codex and Devin (`Quota.Signals`, `NextRecoverAt`; [CPA: quota_signals.go#L16-L24](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/quota_signals.go#L16-L24), [types.go#L175](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/types.go#L175)). None of it reaches the plugin. The candidate's `Metadata` field is documented but never filled ([CPA: conductor_selection.go#L850-L868](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L850-L868)). `host.auth.get_runtime` returns only `next_retry_after` ([CPA: types.go#L752-L812](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/pluginapi/types.go#L752-L812)). So every plugin above fetches reset times on its own, and each covers only the providers it wrote a parser for.
2. **No access to CPA's affinity.** Returning an ID or delegating skips `SessionAffinitySelector`. The `host.affinity.lookup` callback returns `unsupported` whenever any scheduler plugin is installed ([CPA: conductor_selection.go#L457-L466](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L457-L466), [affinity_callbacks.go#L42](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/internal/pluginhost/affinity_callbacks.go#L42)). A plugin that wants affinity must keep its own bindings.
3. **No request body.** The pick gets headers and metadata, not the original request, so it cannot see thread IDs carried in the body (such as Claude Code's `metadata.user_id`) or use CPA's prefix matching. claude-seat-pacer works around this by deriving a key in a before-auth interceptor and passing it on as a header.
4. **One scheduler plugin at a time.** The host uses the first active plugin with the capability ([CPA: internal/pluginhost/scheduler.go#L45-L57](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/internal/pluginhost/scheduler.go#L45-L57)). Scheduler plugins cannot be chained, so an expiring-first plugin cannot run beside quota-activation, an autoban plugin or a key-binding plugin.
5. **Only the candidates it is given.** It cannot pick a disabled, cooling-down, already-tried or other-provider credential, or a lower tier without `SchedulerAcrossPriorities`. It cannot switch provider or model; that happens earlier, in model routing.
6. **Not used in Home mode.** When CLIProxyAPIHome is enabled, selection goes to Home and the plugin is never called ([CPA: conductor_selection.go#L1980-L1985](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L1980-L1985)).
7. **It replaces the host's fast path.** Installing any scheduler plugin sends every pick through the legacy selection path instead of the built-in scheduler's fast path ([CPA: conductor_selection.go#L1989](https://github.com/bandoyer/CLIProxyAPI/blob/27db85455dddfd078e212baa54008ad9c07126b8/sdk/cliproxy/auth/conductor_selection.go#L1989)).

## Could one of these, or a new plugin, provide expiring-first routing for every provider?

**None of the existing plugins can.** Each one hard-codes quota parsing for one to four providers. The broadest is quota-pacer (Antigravity, Codex, Claude, xAI), which paces by headroom through weights rather than preferring the credential that would lose the most at its next reset. The closest strict rules are codex-quota-scheduler's "percent left per hour to reset" for Codex and quota-reset-router's "earliest weekly reset" for Claude and Codex. Neither keeps affinity.

**A new scheduler plugin could, with limits.**

- It can rank any provider's credentials by "quota that would be lost at the next reset", as long as that provider reports usage and reset times in headers or a usage endpoint. For providers that report neither, it has nothing to rank on.
- It must collect that data itself, because the host does not pass its own quota observations to plugins.
- It must rebuild affinity itself, as claude-seat-pacer does, because taking the pick bypasses CPA's affinity. Thread keys that live in the request body need a before-auth interceptor.
- It occupies the only scheduler slot.

**A priority- or weight-writing plugin is the other route.** It keeps CPA's affinity for free, but it acts only as often as it polls, writes auth files on every change, and cannot express "this credential, for this new thread, now".

Doing expiring-first routing in the host would remove most of these limits: the host already has the quota signals, the affinity bindings and the request body at selection time.

## Open questions

- Should the host pass its quota observations (`Quota.Signals`, `NextRecoverAt`) and the affinity binding into `SchedulerPickRequest`? That would let a plugin do expiring-first routing without its own pollers and bindings. This is a fork change to `sdk/cliproxy/auth/conductor_selection.go` and `sdk/pluginapi/types.go`.
- Which providers report reset times at all is answered by [#3](https://github.com/bandoyer/CLIProxyAPI/issues/3). Where the rule should live (core selector or plugin) is [#10](https://github.com/bandoyer/CLIProxyAPI/issues/10).
- Does claude-seat-pacer's interceptor-to-header handoff stay reliable on this fork's v8 host? It was tested on 8.0.4 by its author; not verified here.
