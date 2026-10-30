# Exports

```sh
gg export --format json > usage.json                       # everything, one document
gg export --format csv --by model > models.csv             # one table
gg export --format csv --by events > calls.csv             # every API call
```

All [filters](GUIDE.md#filters) apply. Progress and warnings go to **stderr**, so stdout is always clean data.

**Exports contain working-directory paths and session ids.** Look before you share them.

## Conventions

- **`null` / an empty CSV cell means "not reported" or "no price" — never zero.** A real zero is `0`.
- `cost_usd` is the API-equivalent estimate; `cost_status` tells you how far to trust it:

  | `cost_status` | Meaning |
  |---|---|
  | `priced` | every token had a known price |
  | `partial` | some tokens had no price: `cost_usd` is a **lower bound** |
  | `unpriced` | nothing could be priced: `cost_usd` is `null` |
  | `none` | no tokens |

- `total_tokens = input + cache_read + cache_write + output`; `reasoning_tokens` is a subset of `output_tokens`.
- CSV cells beginning with `=`, `+`, `-` or `@` are prefixed with `'` so a hostile folder name cannot become a
  spreadsheet formula.

## JSON

One object:

| Key | Contents |
|---|---|
| `generated` | UTC timestamp |
| `range` | `name`, `from`, `to_exclusive` (UTC; `null` for all time), `timezone` (IANA name, e.g. `Europe/Madrid`; `null` when only the system's local time is known), `utc_offset` (the offset at the start of the period, e.g. `+02:00`; at now for all time) |
| `filters` | the non-date filters, as text |
| `partial` | `true` when the scan was cancelled and the totals cover only the files read; `false` otherwise |
| `partial_reason` | why the export is partial; only present when `partial` is `true` |
| `total` | one *group* (below) for the whole selection |
| `previous_period` | a group for the comparison window (only for bounded ranges) |
| `by_day`, `by_provider`, `by_model`, `by_project`, `by_session` | arrays of groups, largest first (`by_day` is chronological and has one entry for every day of the range, days with no usage included: `total_tokens` 0, `cost_status` `none`) |
| `sources` | per tool: `id`, `name`, `status`, `files`, `detail` |
| `parse` | `malformed`, `clamped`, `oversized`, `duplicates_merged`, `unreadable_files` |
| `notes` | the standing caveats (cost is an estimate; null means not reported; the total formula) |
| `prices` | the price sources with their dates |

A **group** (illustrative values):

```json
{
  "key": "codex/gpt-6-sol",
  "label": "gpt-6-sol",
  "sessions": 11,
  "calls": 412,
  "input_tokens": 1830221,
  "cache_read_tokens": 17021400,
  "cache_write_tokens": null,
  "output_tokens": 210511,
  "reasoning_tokens": 61230,
  "total_tokens": 19062132,
  "cost_usd": 17.61,
  "cost_status": "priced"
}
```

`unpriced_models` is present (and non-empty) when part of a group could not be priced.

## CSV

`--by` only applies with `--format csv`; `--format` only applies to `gg export`.

`--by day|provider|model|project|session` writes one row per group with the columns

```
key,label,sessions,calls,input_tokens,cache_read_tokens,cache_write_tokens,output_tokens,reasoning_tokens,total_tokens,cost_usd,cost_status
```

`--by day` has one row for every day of the range, including days with no usage (zero tokens, `cost_status` `none`).

`--by events` writes one row per API call (after de-duplication):

```
time,provider,model,session,project,input_tokens,cache_read_tokens,cache_write_tokens,output_tokens,reasoning_tokens,total_tokens,cost_usd
```

(`time` is UTC, RFC 3339; an unpriced call has an empty `cost_usd`.)

## Recipes

```sh
# total tokens and the cost caveat
gg export --format json | jq '{tokens: .total.total_tokens, cost: .total.cost_usd, status: .total.cost_status}'

# models by estimated cost, flagging the unpriced ones
gg export --format json | jq -r '.by_model[] | [.label, (.cost_usd // "unpriced"), .cost_status] | @tsv'

# cache share per provider (only where the tool reports cache reads)
gg export --format json | jq -r '.by_provider[] | select(.cache_read_tokens != null)
  | [.label, (.cache_read_tokens / (.input_tokens + .cache_read_tokens + (.cache_write_tokens // 0)))] | @tsv'

# daily series for a chart
gg export --format csv --by day --days 90 > daily.csv

# which tools were actually read, and what was skipped
gg export --format json | jq '.sources, .parse'
```
