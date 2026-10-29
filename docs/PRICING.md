# Pricing

`gg` prices usage at **public API list prices, per 1M tokens, in USD**. The table is embedded in the
binary (`internal/pricing/prices.json`) so it works offline, and every price carries a source, URL and
update date. Inspect it with:

```sh
gg prices               # table, sources, dates
gg prices --unpriced    # + models in your history that have no price
```

## How a model is matched

The model id is normalised (lower-case, provider prefix and `[1m]` suffix removed, trailing snapshot
date removed) and matched **exactly**, or by id prefix at a `-` boundary (so
`claude-sonnet-5-5-20261001` → `claude-sonnet-5-5`; the *longest* match wins, so `-5-5` never falls back to
`-5`). Anything else is **unpriced**: its tokens are counted, its cost is excluded, and the report
shows `unpriced` or `≥ $X` (a lower bound). `gg` never guesses a price from a similar-looking name.

## How cost is computed

```
cost = uncached_input × input
     + cache_read     × cache_read
     + cache_write_5m × (1.25 × input)      Anthropic
     + cache_write_1h × (2 × input)         Anthropic
     + output         × output
```

OpenAI does not bill cache writes separately. Reasoning tokens are part of output and are not billed
again. Not modelled: batch/flex/priority tiers, long-context surcharges, discounts, subscriptions.

## Sources and freshness

| Source | Updated | Notes |
|---|---|---|
| Anthropic API pricing | 2026-10-06 | Cache-write multipliers from the documented convention. Where a model's cache-read price is not listed it is derived as 0.1× input and marked `derived` in `gg prices`. Haiku 5.5 uses the ≤100K-token prompt rate. |
| Gemini API pricing | 2026-10-29 | Standard tier. Retrieved through a page summariser — verify against the URL. Prompts over 200K tokens cost more for some models and are **not** modelled (cost is a lower bound there); announced price increases are noted per model. Cache storage fees are not modelled. |
| OpenAI API pricing | 2026-10-29 | Standard tier, context ≤ 272K. **Retrieved through a page summariser** — verify against the URL before relying on exact figures. |

Prices change. Update `internal/pricing/prices.json` (and its `updated` date) in a PR, or override
locally as below.

## Adding or overriding prices

Create `<config dir>/gauge-and-grain/prices.json` (path shown by `gg sources`) or pass `--prices FILE`.
Entries replace embedded ones with the same id.

```json
{
  "sources": [{"id": "me", "name": "My rates", "url": "https://example.com", "updated": "2026-10-29"}],
  "models": [
    {"id": "codex-auto-review", "source": "me", "input": 1.75, "output": 14.00, "cache_read": 0.175},
    {"id": "gpt-5.4", "source": "me", "input": 2.00, "output": 12.00}
  ]
}
```

Optional per-model fields: `cache_read`, `cache_write_5m`, `cache_write_1h` (USD per 1M),
`note`. A broken override file prints a warning and is ignored; the embedded table still loads.
See [examples/prices.json](../examples/prices.json).
