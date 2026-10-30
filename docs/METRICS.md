# What the numbers mean

`gg` puts several tools on one scale. This page defines that scale, so you can tell what is being compared and
what is not.

## One definition of "tokens"

Every tool reports tokens a little differently, so each parser **normalises** into five fields:

| Field | Meaning |
|---|---|
| **Input** | prompt tokens that were *not* served from a cache |
| **Cache read** | prompt tokens reused from a cache (cheap) |
| **Cache write** | tokens stored into a cache (priced above input by Anthropic; not billed separately by OpenAI) |
| **Output** | every generated token, **including** reasoning/"thinking" tokens |
| **Reasoning** | the reasoning part of Output — informational, **never added again** |

```
total tokens = input + cache read + cache write + output
```

The same formula is used for every provider, so shares and rankings across providers are comparable. What differs
is *which fields a tool reports*, and that is never hidden: a field a tool does not record is shown as **n/a**
(`null` / an empty cell in exports), not as `0`.

### How each tool maps onto it

| Tool | What the log says | Normalisation |
|---|---|---|
| **Claude Code** | `input_tokens` excludes cache; separate cache read/creation; `thinking_tokens` ⊂ output | used as-is. Cache writes with a 1-hour TTL are priced at the 1-hour rate. |
| **Codex** | `input_tokens` **includes** cached tokens; reasoning ⊂ output | Input = input − cached (− cache write); Cache read = cached |
| **Gemini CLI**, **Qwen Code** | prompt **includes** cached; output **excludes** thoughts; tool-use prompt is input | Input = (prompt − cached) + tool; Output = output + thoughts |
| **Pi**, **Cline family**, **DeepSeek** | input excludes cache; cache counts separate | used as-is (DeepSeek reasoning treated as ⊂ output) |
| **OpenCode / Kilo CLI** | input excludes cache; output excludes reasoning | Output = output + reasoning |

Details, assumptions and which of these are verified: [PROVIDERS.md](PROVIDERS.md).

## What is counted, and once

- **API calls** — one per billed model response. Claude Code writes the same response once per content block, and
  resumed/forked sessions repeat earlier messages; each response is counted **once**. Within one file, Claude and
  Pi keep the largest counter seen, Codex and DeepSeek keep the first copy, and Gemini and Qwen keep the last. When
  the same response is in several files, the scan merges the copies and keeps the largest counter.
- **Codex** — newer logs give exact per-response usage (preferred); older logs only have cumulative counters, of
  which only the *increase* is used, so the turn-end re-broadcast of the same total adds nothing. Archived sessions
  are de-duplicated by response id.
- **Sessions** — distinct provider + session ids with at least one call in the selected range.
- **Projects** — the working directory of the session (the last two path elements are shown). Some tools do not record
  one: those show as `(unknown)` (the Cline family) or as the tool's own project id (`gemini:<id>`, `qwen:<dir>`).
- **Days** — your local time zone.

## Estimated cost

```
cost = uncached input × input price + cache read × cache-read price
     + cache write × cache-write price + output × output price
```

at **public API list prices** per 1M tokens (see [PRICING.md](PRICING.md) for the sources and dates).

- It is an **API-equivalent estimate**. It is useful to compare models, projects and periods. It is **not your
  bill**: subscriptions (Claude Pro/Max, ChatGPT plans, free tiers) are priced differently.
- A model with **no known price is `unpriced`**. Its tokens are counted; its cost is left out and the total becomes a
  lower bound, written **`≥ $X`** (and the row says `partial`). Prices are never guessed from similar names.
- Not modelled: batch/flex/priority tiers, long-context surcharges (e.g. Gemini prompts over 200K), cache storage fees,
  discounts, taxes.

## Comparing

- **vs previous period** — the change in total tokens (and in cost, when the selection is fully priced) against the
  window just before. Rolling periods use the window of equal length; calendar periods use the previous calendar
  period. It is shown only for a bounded period. `>1000%` means "up by more than 1000%"; no figure is shown when the
  previous period has no usage.
- **Share** — a row's tokens as a fraction of the selected total.
- **Cache hit share** — cache read ÷ (input + cache read + cache write): how much of the prompt-side traffic came from
  cache. Shown only when a source reports cache reads.
- **Trend** — the daily chart is tokens per day, with days of zero usage filled in so it is not visually compressed.
  "Second half vs first half" appears only for ranges with at least 7 active days.
- **Fair comparisons** — shares across providers use the same token definition; the Notes view lists, per provider,
  the fields it does not always report, so you know when two numbers mean slightly different things.

## Data quality counters

The report counts what it could not use, and says it did not use it: **malformed** records (not valid JSON or the wrong
shape, including a half-written final line), **clamped** counters (negative or absurdly large values treated as 0),
**oversized** lines (over 64 MB), **repeated** records counted once, and **unreadable** files. None of them is added
to a total.
