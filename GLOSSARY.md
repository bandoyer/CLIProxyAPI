# CLIProxyAPI

A proxy that pools many provider subscriptions behind one endpoint, so coding clients can spend all of their quota without manual account switching.

## Language

### Pool

**Provider**:
An upstream AI service that the proxy sends requests to, such as Claude, Codex, xAI, or Kimi.
_Avoid_: backend, vendor

**Credential**:
One signed-in subscription or API key for a provider, which the proxy can send requests through.
_Avoid_: account, auth file, model

**Quota window**:
A period over which a provider limits how much one credential can use, such as the Claude 5-hour and 7-day limits or the Codex weekly limit.
_Avoid_: session, limit period

**Reset time**:
The moment a quota window ends and the credential's usage in that window returns to zero.
_Avoid_: session end, renewal

### Routing

**Thread**:
One ongoing conversation in a client, whose prompts share context and build on each other.
_Avoid_: session, chat

**Affinity**:
The rule that sends every request in a thread to the same credential while that credential stays usable.
_Avoid_: sticky routing, account affinity, session affinity

**Expiring-first routing**:
The rule that, among usable credentials, prefers the one with the highest urgency, so the quota that would be lost at a reset time is spent first.
_Avoid_: smart routing, intelligent routing

**Urgency**:
The share of a credential's quota window still left, divided by the time until its reset time: how fast the credential must be used to lose nothing at reset.
_Avoid_: score, burn rate, pace

**Cache write**:
A request that makes the provider store a new prompt prefix for reuse, instead of reading one it already stored.
_Avoid_: cache miss, re-read
