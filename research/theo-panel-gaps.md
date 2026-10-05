# What Theo's panel shows that the stock panel and plugins don't

Research for [What does Theo's panel show that the stock panel and plugins don't?](https://github.com/bandoyer/CLIProxyAPI/issues/29), part of the map [issue #2](https://github.com/bandoyer/CLIProxyAPI/issues/2). Researched on 2026-10-05.

## Summary

Theo's panel is the stock management panel (CPAMC) from about 12 September 2026, with one page changed: **Quota Management**. The sidebar, the Auth Files badge, the provider tabs with counts and the whole Config Panel are stock. Every value on Theo's quota page is a value that stock CPAMC already fetches, so the gaps are in layout and aggregation, not in data.

The gaps, all on the Quota Management page:

1. **Per-provider summary cards.** One card per provider that adds up the share left across all its credentials.
2. **Ledger layout.** Credentials as rows grouped under provider headings, with quota windows as side-by-side columns and the actions on the right.
3. **"Ledger" selector.** A drop-down at the top right of the quota page. Its options can't be seen in the stills.
4. **Compact per-credential text.** "No reset pending" for windows without a reset time, relative-first reset times, a single "Pro 20x · renews 10/03, 17:02 · in 21 days" line, and a compact manual-reset line.

No store plugin provides any of these. None needs a new management API endpoint: the panel already reads the data through the credential list and the `api-call` proxy endpoint.

## Sources

- Theo's screenshots: three video stills in `~/Pictures/theo-panel/` on `omarchy-desktop`. Two show `#/quota` and one shows `#/config`. They are not in the repo because they show a private fork.
- Stock panel source: [router-for-me/Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center), `main` at `ee79a79` (3 October 2026), which is release v1.25.3. History was checked back to August 2026.
- Stock panel as this fork serves it: the preview proxy at `127.0.0.1:8317` downloaded v1.25.3 on 2026-10-05. The served `management.html` contains the same strings as the source.
- Plugin store index: `https://raw.githubusercontent.com/router-for-me/CLIProxyAPI-Plugins-Store/main/registry.json` (`internal/pluginstore/registry.go:15`). It lists 108 plugins, and the source of 27 quota- and panel-related plugins was checked.
- This fork's management API: `internal/api/server_management.go`, `internal/api/server_management_v8.go`, `internal/api/handlers/management/`, and `docs/management-api-v8.md`.

## Which stock release Theo's panel is built on

This fork downloads the **latest** CPAMC release (`internal/managementasset/updater.go:29`, `.../releases/latest`), which is v1.25.3 today. Classes (a) and (b) below are therefore the same thing: anything in a newer CPAMC release reaches this fork on the next panel update.

Theo's build is a CPAMC fork from about 12 September 2026, between v1.22.16 (12 September) and v1.23.0 (13 September):

- Reset times in the stills fall between 09/11 and 09/17, and the Codex renewal is 10/03 "in 21 days", so the video was recorded around 11 to 12 September.
- The quota tabs are All, Claude, Antigravity, Codex, xAI and Kimi, which is the stock tab order (`src/features/quota/constants.ts`) before Devin (added 14 September) and Meta (added 16 September).
- The Codex plan reads "Pro 20x". This was the stock label until stock renamed it to "Pro 200" on 3 October (`ee79a79`).
- The page has no account search box, which stock added on 20 September (`6869fd2`).
- In the stock page of that time, the sort drop-down sat at the right end of the tabs row (`8d7b869`), which is where Theo's "Ledger" drop-down sits.

## Classification

Classes: **(a)** in the stock panel release this fork downloads (v1.25.3); **(b)** only in a newer CPAMC release; **(c)** provided by a store plugin; **(d)** missing.

Class (b) is empty because the fork always downloads the latest release.

### Sidebar and shell

| Item in Theo's stills | Class | Evidence |
|---|---|---|
| Brand "CPAMC / CLI Proxy API Console" | (a) | `en.json` key `…subtitle = "CLI Proxy API Console"` |
| Sidebar groups Operate, Gateway, Observe, Control, with Dashboard, Quick Start, AI Providers, Auth Files, OAuth Login, Quota Management, Logs Viewer, Config Panel, Plugins, Plugin Store | (a) | `nav_groups.*` and `nav.*` in `en.json`; routes in `src/router/MainRoutes.tsx` |
| "Management Center ..." entry | (a) | Truncated `nav.system_info = "Management Center Info"` (route `/system`) |
| Auth Files count badge ("11") | (a) | `src/components/layout/MainLayout.tsx:623` |
| Collapse arrow on the sidebar edge | (a) | Stock sidebar toggle (Cmd/Ctrl+B, `97fbadd`) |

### Quota Management page

| Item in Theo's stills | Class | Evidence and notes |
|---|---|---|
| Provider tabs with counts ("All 10", "Claude 5", "Antigravity 0", "Codex 3", "xAI 1", "Kimi 1") | (a) | `ProviderTabs` plus `buildTabCounts` in `src/features/quota/logic.ts`. Stock today also shows Devin and Meta tabs. |
| **Per-provider summary cards** ("Claude · 5 credentials · 7-day Fable 5 · 409% of 500%", segmented bar, "in 1 day · 09/12, 23:00", "7-day limit 454% · Show") | **(d)** | No stock equivalent. Stock has a page header with counts (total, loaded, need attention) and a "Quota windows" timeline, but no per-provider aggregate. See [Gap 1](#gap-1-per-provider-summary-cards). |
| **"Ledger" drop-down** at the top right | **(d)** | No string "Ledger" anywhere in CPAMC history (only a code comment in `resetGrantOperations.ts`). See [Gap 3](#gap-3-the-ledger-selector). |
| **Rows grouped under provider headings** ("Claude 5", "Codex 3", "xAI 1"), with each quota window as a column | **(d)** | Stock shows a grid of cards. In the "All" tab the cards are ordered by provider, but there are no headings and the windows are stacked inside each card (`QuotaCard.tsx`). See [Gap 2](#gap-2-ledger-layout). |
| Claude windows "7-day Fable 5", "5-hour limit", "7-day limit", each with % left, a bar and a reset time | (a) data, (d) layout | `claude_quota.seven_day_fable`, `five_hour`, `seven_day` (Fable since `50c3b9f`, 28 July). Data comes from `api.anthropic.com/api/oauth/usage` through `api-call`. |
| Claude plan line "Max" | (a) | `claude_quota.plan_max` (from `/api/oauth/profile`) |
| **"No reset pending"** on windows that have no reset time | **(d)**, cosmetic | Stock's `buildResetDisplay` returns `null`, so nothing is shown (`src/utils/quota/relativeTime.ts:120`). |
| Reset time written as "in 4 days · 09/15, 22:00" (relative first) | (d), cosmetic | Stock writes "08-13 14:30 · in 11 days", absolute first (`QuotaResetLabel.tsx`). |
| Codex "Weekly limit" window | (a) | `codex_quota.secondary_window`, data from `chatgpt.com/backend-api/wham/usage` |
| Codex plan and renewal **as one line**: "Pro 20x · renews 10/03, 17:02 · in 21 days" (and "4 days ago", "42 days ago" for past dates) | (a) data, **(d)** layout | Stock shows the same values as labelled chips, "Plan" and "Renewal time" (`CodexQuotaBody.tsx`). The renewal date is fetched live from `backend-api/subscriptions` (`bbac79d`), and the credential list also has `id_token.chatgpt_subscription_active_until`. |
| Codex **"Manual resets · 2 available · Reset 1 · in 22 days · 10/03, …"** | (a) data, (d) layout | Stock shows the "Manual resets" count and a "Manual reset expiry" list (Reset 1, Reset 2, …) from `wham/rate-limit-reset-credits` (`f3959a0`, 12 June). Theo shows the count and only the first expiry, on one line. |
| Codex **"Reset quota"** button | (a) | `codex_quota.reset_button`. It consumes one manual reset through `wham/rate-limit-reset-credits/consume` after a confirmation. |
| xAI "Weekly limit --" and **"Monthly credits $0.00 / $0.00"** | (a) | `xai_quota.weekly_limit` and `xai_quota.monthly_credits` (since `9a5c2b0`, 24 May), from `api.x.ai` billing through `api-call` |
| Kimi "Weekly limit" | (a) | `kimi_quota.weekly_limit` |
| Per-credential **"Refresh quota"** button | (a) | `auth_files.quota_refresh_single`, in the footer of each stock card |
| Circular button at the far top right, partly hidden behind Theo's webcam | (a), probably | Probably the stock global header refresh (`useHeaderRefresh`). It is too hidden to confirm. |
| Every credential already loaded | Unknown | Stock cards start idle and load when clicked or when "Refresh all credentials" is pressed. The stills can't show whether Theo's page loads by itself. |

### Config Panel

Everything in the Config Panel still is stock, class (a).

| Item | Evidence |
|---|---|
| Search box "Search settings (label or YAML key)" | `config_management.visual.search.placeholder`, `ConfigSearch.tsx`, `searchIndex.ts` |
| Tabs: Common, Access & Authentication, Network Configuration, Logging & Diagnostics, Quota Fallback, Streaming C… (cut off) | `CONFIG_TAB_IDS` in `src/features/config/constants.ts`. The cut-off tab is "Streaming Configuration", and stock has two more tabs after it, Advanced and Payload. Tabbed panel since `c2feeac` (6 August). |
| "Common: The most-used settings, backed by the same data as the full sections" | `sections.common.description` |
| Common fields: Host Address, Port, Proxy URL, API Keys List with "Add API Key", Debug Mode, Log to File, Switch Project, Switch to Preview Model | Exactly the eight `COMMON_FIELD_IDS` |
| "No suitable proxy? BestProxy.com" next to Proxy URL | Stock sponsor hint, `src/features/config/sponsors.ts` |

## Gap details

### Gap 1: per-provider summary cards

**What it shows.** One card per provider with credentials, in a row under the tabs. Each card has:

- the provider name and icon, and the number of credentials ("5 credentials");
- a headline quota window name ("7-day Fable 5" for Claude, "Weekly limit" for Codex, xAI and Kimi);
- the **sum of the share left** in that window across the provider's credentials, out of 100% per credential. For Claude, 58 + 100 + 100 + 51 + 100 = **409% of 500%**. For Codex, 17 + 0 + 0 = 17% of 300%. Both totals match the rows in the stills.
- a **segmented bar with one segment per credential**, each coloured by that credential's level (Claude: yellow, green, green, yellow, green, which matches 58, 100, 100, 51, 100);
- the **soonest reset time** among the provider's credentials ("in 1 day · 09/12, 23:00", the earliest Claude Fable reset; "in 2 days · 09/14, 18:23", the earliest Codex reset);
- a second total for another window with a **"Show" toggle** ("7-day limit 454%" = 79 + 100 + 100 + 75 + 100). "Show" probably expands the card to list every window.
- xAI shows "-- of 100%" because its weekly reading is empty.

**Data it needs.** The same quota readings that each stock quota card already fetches for each credential, summed in the browser. It needs no new endpoint. It does need readings for *all* of a provider's credentials, but stock fetches a credential only when its card is clicked or "Refresh all credentials" is pressed, and only for the current page of 20.

**Does this fork's API provide it?** Yes, indirectly. The readings come from provider usage endpoints called through `POST /v0/management/api-call` (`/v8/management/requests/api-call`, `internal/api/handlers/management/api_tools.go:49-96`). No endpoint returns quota readings already gathered by the proxy. `GET /v8/management/credentials` carries only raw passive header values (`quota.signals`), and only for Claude, Codex and Devin credentials that have served traffic (`sdk/cliproxy/auth/quota_signals.go`). `GET|POST /v0/management/quota/*` works only with a QuotaProvider plugin installed.

**Stock, newer release or plugin?** None. The closest plugins, quota-center and aggregate-usage, show one card per credential or aggregate across proxy nodes, not across credentials.

### Gap 2: ledger layout

**What it shows.** In place of the card grid, a list. Each provider gets a heading with a count ("Claude 5"). Each credential is a row with:

- the file name and a plan line on the left;
- one column per quota window, each with a label, % left, a coloured bar and a reset time;
- for Codex, a "Manual resets" column;
- "Reset quota" and "Refresh quota" on the right.

All of a provider's credentials can be compared down a column.

**Data it needs.** Exactly what the stock cards render. This is a presentation change only.

**Does this fork's API provide it?** Yes, as for the stock cards.

**Stock, newer release or plugin?** None. quota-center draws cards on its own plugin page.

### Gap 3: the "Ledger" selector

**What it shows.** A drop-down labelled "Ledger" at the top right of the quota page, in the tabs row. Stock had its sort drop-down in that place at the time ("Default" or "Soonest recovery").

**What it does.** This can't be determined from the stills. The likeliest reading is a view switch between the ledger layout and another view, such as the stock cards or the stock timeline. It could also be a sort or grouping choice. Theo's video, or a frame with the drop-down open, would settle it.

**Data it needs.** Probably none beyond the gaps above.

**Stock, newer release or plugin?** None.

### Gap 4: compact per-credential text

These are small wording and formatting differences on data that stock already has:

- **"No reset pending"** where a window has no reset time. In the stills this is a 5-hour window at 100%, which has not started. Stock leaves the place blank.
- **Relative-first reset times**: "in 3 hours · 09/11, 23:50".
- **One plan line for Codex**: "Pro 20x · renews 10/03, 17:02 · in 21 days". It uses "renews" with a past-tense form ("4 days ago") when the stored renewal date has passed. Stock shows two labelled chips. The stock label is now "Pro 200" for the same plan.
- **Compact manual resets**: "2 available", then only the first expiry, "Reset 1 · in 22 days · 10/03, …". Stock lists every reset credit with its expiry.

**Data it needs.** Already fetched by stock (`wham/usage`, `backend-api/subscriptions`, `wham/rate-limit-reset-credits`), and partly in `GET /v8/management/credentials` (`id_token.plan_type`, `chatgpt_subscription_active_until`). The "20x" tier is not a field in this fork's API. It is the panel's name for plan type `pro`.

## Store plugins

No plugin in the store index provides any of the four gaps.

Plugins cannot change `management.html`. They can only add their own pages under `/v0/resource/plugins/<id>/…`, which the panel lists as extra menu entries (`internal/api/server_management.go:292-308`, `internal/api/handlers/management/plugins.go`). They can also feed normalized quota data to `/quota/*` through `pluginapi.QuotaProvider` (`sdk/pluginapi/types.go:1557-1752`).

Plugins that overlap with features stock already has:

| Plugin | What it covers |
|---|---|
| quota-center | Per-credential quota windows with reset times and a refresh button, plus a Grok "credits" window, on its own page |
| codex-auto-reset, codex-quota-scheduler, codex-fleet-manager | Codex manual-reset counts and a reset action, on their own pages |
| cliproxy-costs | Passive Claude and Codex windows from response headers, on its own page |

A plugin page could host the summary cards and the ledger layout. It would be a separate page, not the stock Quota Management page.

## What Theo's stills don't show

- **What the "Ledger" drop-down does**, and what its other options are.
- **Whether the quota page loads readings by itself.** It could load on open or on a timer, or Theo could have pressed refresh.
- **The headline window rule.** It is unclear how the summary card picks its headline window ("7-day Fable 5" over "7-day limit" for Claude). It may be the most-used window, a fixed per-provider choice, or the first window.
- **What "Show" expands to.**
- **The colour thresholds** for the bars and segments (yellow at 51% and 58%, red at 17%).
- **What sits above the tabs.** The stills could be scrolled, so it is unknown whether Theo removed stock's page header ("Refresh all credentials"), the timeline and the pagination, or whether they are off-screen.
- **Whether the "Reset quota" button differs from stock's**, for example its confirmation step, and whether Theo's panel offers Claude reset grants. Stock added those on 1 October, after the video.
- **Any other changed page.** Only Quota Management and Config Panel appear. Dashboard, Auth Files, AI Providers, Logs, Plugins and Plugin Store are not shown, so other customizations can't be ruled out.
- **Server-side changes in Theo's fork.** Theo's fork may add backend endpoints, for example server-side quota polling feeding the summaries. A still can't show where the data came from.
