# Quota Management page prototype (throwaway)

Prototype for [How should the Quota Management page look and behave?](https://github.com/bandoyer/CLIProxyAPI/issues/30), part of the map [Map: personal multi-provider proxy on the tailnet](https://github.com/bandoyer/CLIProxyAPI/issues/2). Built on 2026-10-05. This branch is a record, not code to merge.

## What it is

Four layouts of the stock panel's Quota Management page, on the real `#/quota` route, chosen with `?variant=` and a floating switcher (← → keys):

| Variant | Layout |
|---|---|
| (none) | Stock CPAMC v1.25.3 |
| A | Theo's ledger: per-provider summary cards, then rows grouped by provider, a "Ledger" drop-down (Ledger or stock Cards) |
| B | Provider panels: every window column header carries that window's total across the provider |
| C | One queue across providers in expiring-first order (urgency = % left in the longest window ÷ hours to reset) |
| D | A's summary cards over B's panels, no drop-down. The variant Dan chose. |

The prototype variants load every Claude and Codex reading once when the page opens. "Reset quota" is disabled so the prototype can't spend a manual reset. Claude renewal dates in D are estimates from `subscription_created_at`; both turned out wrong (see the ticket).

## Run it

`cpamc-quota-prototype.patch` applies to [router-for-me/Cli-Proxy-API-Management-Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) at `ee79a79` (v1.25.3):

```bash
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git cpamc
cd cpamc && git checkout ee79a79 && git am /path/to/cpamc-quota-prototype.patch
npm install && npx vite --host 127.0.0.1 --port 5180
```

The dev server forwards `/v0` and `/v8` to a proxy at `127.0.0.1:8317`. Log in at `http://127.0.0.1:5180` with the management key, then open `#/quota?variant=D`.
