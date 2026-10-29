# Providers: data locations, assumptions, limitations

Every provider has its own adapter in `internal/providers/` with its own parser, a
`ParserVersion()` (part of the cache key) and documented assumptions. Fixtures that exercise each
assumption are in `testdata/`, with expected numbers worked out by hand in the tests.

`gg sources` shows what was found on your machine and why anything is missing.

## Claude Code — supported

**Data:** `~/.claude/projects/**/*.jsonl` and `~/.config/claude/projects/**/*.jsonl`
(or `$CLAUDE_CONFIG_DIR/projects`, a path list). Includes sub-agent transcripts under
`<session>/subagents/`.

**What is read:** `type: "assistant"` records with `message.usage`:
`input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`,
`cache_creation.ephemeral_1h_input_tokens`, `output_tokens_details.thinking_tokens`, plus
`message.model`, `timestamp`, `sessionId`, `cwd`.

**Assumptions**

1. Claude Code writes **one record per content block** of the same API response, repeating
   `message.id` + `requestId` and the usage. Each `(message.id, requestId)` is **one call**, and the
   largest value of each counter is kept (streamed chunks only grow). Verified against a
   17.8k-call real history: totals matched an independent implementation exactly.
2. `input_tokens` excludes cache; cache read/creation are separate fields.
3. `thinking_tokens` is a subset of `output_tokens`.
4. Resumed/forked sessions and sub-agent files repeat earlier messages; the same key in several files
   is counted once (global de-duplication).
5. Records whose model starts with `<` (e.g. `<synthetic>`) are not API calls and are skipped.
6. Project = the record's `cwd`.

**Limitations:** records without a `message.id` and `uuid` cannot be de-duplicated safely and are
counted as malformed. When a record has no 5-minute/1-hour cache TTL breakdown, its cache writes are priced at the
5-minute rate. Usage from Claude.ai chat, Claude Desktop chat, or the
API outside Claude Code is not in these files.

## OpenAI Codex — supported

**Data:** `~/.codex/sessions/**/rollout-*.jsonl` and `~/.codex/archived_sessions/**/*.jsonl`
(or `$CODEX_HOME`).

**What is read:** `session_meta` (session id, `cwd`), `turn_context` (`model`, `cwd`), and token usage:

- **Newer logs:** `token_usage_record` with a per-response `usage` and a `response_id`. These are
  exact and preferred.
- **Older logs:** only `event_msg` / `token_count` with cumulative `total_token_usage`. The delta
  between increasing totals is used; **a repeat of the same total (the turn-end re-broadcast) adds
  nothing**; a total that goes *down* (counter reset) is treated as a fresh counter and the event's
  `last_token_usage` is used.

**Assumptions**

1. OpenAI's `input_tokens` **includes** cached tokens. `gg` reports *uncached input* =
   `input_tokens − cached_input_tokens − cache_write_input_tokens` so providers are comparable.
2. `reasoning_output_tokens` is a subset of `output_tokens`.
3. `cache_write_input_tokens` is assumed to be part of `input_tokens` like cached tokens. It was always
   `0` in the logs this was built against, so this is **unverified**.
4. The model is the latest `turn_context.model` before the event; before any, it is `unknown`.
5. The same `response_id` in several files (archived copy, forked session) is counted once.
6. When a file has any `token_usage_record`, `token_count` events in that file are ignored (they are
   cumulative views of the same usage).

**Limitations:** background/auto-review models appear under their own names and may be unpriced.
A session that was killed mid-write can end in a truncated line; it is reported as malformed.

## Gemini CLI — detected, not parsed

`gg sources` reports whether `~/.gemini/tmp` exists. This release has **no parser** because no
verified sample of Gemini CLI's local usage format was available when it was written; guessing a
format would risk silently wrong totals. If Gemini CLI is present the report says its usage is **not
included**. Adding it means implementing the `Provider` interface (see
[ARCHITECTURE.md](ARCHITECTURE.md)) plus fixtures from a real session.

## Data that cannot be reliably derived

- **Your actual bill.** Logs contain tokens, not invoices, discounts, credits or plan limits.
- **Usage outside local logs:** web chat UIs, other machines, deleted/rotated logs.
- **Gemini CLI and other tools** that have no parser yet.
- **Per-request tier/latency/batch pricing:** logs do not reliably say which price tier applied.
- **Exact cache-TTL pricing** when a log omits the TTL breakdown.
- **Project identity beyond the working directory** (two repos in the same folder look the same;
  one repo opened from two paths looks like two projects).
