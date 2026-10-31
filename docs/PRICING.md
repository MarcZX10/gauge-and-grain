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
date removed, and for Claude ids a dot between version digits becomes a hyphen, so `claude-sonnet-4.6`
is `claude-sonnet-4-6`). The result must match a row's id or one of its explicit `aliases` **exactly**.
There is no prefix matching: `claude-sonnet-5-5-20261001` matches `claude-sonnet-5-5` only because the date
is removed, and `gpt-5-nano` is not `gpt-5`. Anything else is **unpriced**: its tokens are counted, its cost
is excluded, and the report shows `unpriced` or `≥ $X` (a lower bound). `gg` never guesses a price from a
similar-looking name.

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
| Anthropic API pricing | 2026-10-06 | Cache-write multipliers from the documented convention. Where a model's cache-read price is not listed it is derived as 0.1× input and marked `*` in `gg prices`. Haiku 5.5 uses the ≤100K-token prompt rate. |
| Gemini API pricing | 2026-10-29 | Standard tier. Retrieved through a page summariser — verify against the URL. Prompts over 200K tokens cost more for some models and are **not** modelled (cost is a lower bound there); announced price increases are noted per model. Cache storage fees are not modelled. |
| OpenAI API pricing | 2026-10-29 | Standard tier, context ≤ 272K. **Retrieved through a page summariser** — verify against the URL before relying on exact figures. |

Prices change. Update `internal/pricing/prices.json` (and its `updated` date) in a PR, or override
locally as below.

## What-if prices

`gg report --what-if FILE` loads a second prices file, in the same format and with the same checks as `--prices`,
and adds one line under the cost: `What-if prices (FILE): $X (Y% vs current)`. The rest of the report is unchanged.
If the range has unpriced models, the what-if cost is marked `≥` and the change reads `n/a`, as the cost row does. Use it to see what the same usage would cost at
other rates before you change `prices.json`. `gg export` does not take `--what-if`.

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
`note`, `aliases` (other ids priced at this row, matched exactly) and `provider`. Set
`"provider": "anthropic"` on an Anthropic row so its cache writes default to 1.25× and 2× input; without it
cache writes cost nothing unless given explicitly. Negative, NaN or infinite prices and any currency other
than `USD` are rejected.

A broken override file is an error: `gg` stops and names the file, rather than silently using the embedded
prices. A missing `--prices` file is an error for the same reason.
See [examples/prices.json](../examples/prices.json).
