# Expiring-first routing is a core selector, not a scheduler plugin

This fork usually prefers plugins over core changes so that upstream merges stay easy. Expiring-first routing is the exception: it is a new selector in core, chosen by `routing.strategy`, and the affinity selector wraps it in the same way that it wraps round-robin today. A scheduler plugin cannot see the proxy's quota readings or affinity bindings, so the plugin route would need more core changes (passing both through the plugin API) and its own copy of affinity, and it must be the only scheduler plugin installed. A selector in its own file, with a separate module that turns each provider's signals into quota readings, keeps the merge surface to a few lines in config and selector wiring.

## Consequences

- Any selector that is not built in turns off the scheduler fast path, so every pick takes the slower legacy path. Session affinity already does this, and a personal proxy can afford it.
- An installed scheduler plugin overrides the selector, so none can be installed alongside expiring-first routing.
