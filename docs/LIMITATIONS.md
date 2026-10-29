# Known limitations

Everything `gg` cannot do, cannot know, or does only approximately. If a number is affected, the app says so (see
[Notes](GUIDE.md#notes)); this page is the full list.

## Verification status of the parsers

- **Only Claude Code and Codex are verified** against real logs (including an independent re-computation of the
  totals). Every other tool is **experimental**: its parser follows the tool's documented format, was cross-checked
  against [splitrail](https://github.com/Piebald-AI/splitrail)'s open-source analyzers, and is tested with
  **synthetic** logs. No real log from them was available. See [PROVIDERS.md](PROVIDERS.md).
- Specific unverified assumptions: how Codex's `cache_write_input_tokens` relates to `input_tokens`; whether
  OpenCode's `output` excludes reasoning (assumed yes, from its source) and whether DeepSeek Harness's
  `reasoningTokens` is part of `outputTokens` (assumed yes, as in DeepSeek's API); that Cline's `tokensIn` is uncached
  input for every backend.
- Tools may change their log format. Records that no longer parse are skipped and counted as malformed, not guessed,
  so a format change shows up as missing usage plus a malformed count — check `gg notes`.

## Not supported

Grok, Copilot CLI, Copilot in VS Code, Antigravity, Piebald — each for a concrete reason
([PROVIDERS.md](PROVIDERS.md#not-supported-and-why)). Cursor, Aider, Windsurf's own agent and others have no parser.
Usage that never reaches a local log (web chat UIs, other machines, deleted logs) is invisible.

## What cannot be derived reliably

- **Your actual bill** — logs hold tokens, not invoices, plan limits, credits or discounts.
- **Which price tier applied** — batch, flex, priority and fast modes are not recorded reliably.
- **Cache-TTL pricing when a log omits the TTL breakdown** — such cache writes are priced at the 5-minute rate.
- **Project identity beyond the working directory** — two repos in one folder look the same; one repo opened from
  two paths looks like two projects. Cline/Roo/Kilo/Zoo record no directory (projects show as `(unknown)`); Gemini
  and Qwen record only a hashed or sanitised id.
- **Which model served a request** when a tool records it only per task: Roo, Kilo and Zoo attribute a whole task
  to the last model used in it.
- **Tool calls** are only counted from Claude Code transcripts. Codex records its tool calls too, but gg does not read
  them yet, so the Tools view does not include Codex or any other tool.

## Cost

- **List prices, standard tier only.** Not modelled: batch/flex/priority, long-context surcharges (Gemini prompts
  over 200K tokens cost more), cache storage fees, discounts, taxes, free tiers, subscriptions.
- **Unknown models are unpriced** (never guessed); Qwen is unpriced because its prices vary by region and input-length
  tier and `qwen3-coder-plus` is not listed on the page used.
- Some Claude cache-read prices are derived (0.1× input) where the vendor table lists none; cache-write prices use the
  documented 1.25×/2× multipliers. OpenAI and Gemini prices were retrieved through a page summariser: check the source
  URLs before relying on exact figures. `gg prices` marks derived values and shows dates.
- Prices change. Update `internal/pricing/prices.json` (with source and date) or override locally.

## Time and scope

- Date filters and daily buckets use **whole days in your local time zone**; there is no intra-day filtering.
- "vs previous period" exists only for a bounded period, and shows nothing when the previous window has no usage.
- Sessions are counted per provider + session id; a tool that reuses ids across projects would merge them.

## Performance and memory

- Every API call is kept in memory so totals stay exact (no sampling or event caps): about **180 bytes per call**
  (1 million calls ≈ 180 MB).
- Starting from a warm cache costs about two seconds per million calls (decoding the cache). The first scan is limited by
  disk speed (roughly 200 MB/s on the development machine).
- A log that has only grown is resumed from the last complete line; a log that was rewritten (or a single-document file
  or database that changed) is re-read in full.

## Platforms and terminals

- Developed and used on **Linux**. The Windows and macOS builds are cross-compiled and their tests run in CI, but they
  have seen no real-world use yet; the Windows/macOS log locations come from each tool's documentation and were not
  verified on those systems.
- The dashboard needs a UTF-8 terminal of at least 40×8 for the best result (`--ascii` helps with limited fonts).
  Mouse support depends on the terminal and multiplexer.
- The `p` key cycles through every supported provider, including those not installed.

Did you hit something that is not listed? Please [open an issue](https://github.com/MarcZX10/gauge-and-grain/issues).
