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

## Experimental providers

These parsers were written from the **documented or third-party-described** formats of each tool,
cross-checked against the open-source (MIT) analyzers in
[Piebald-AI/splitrail](https://github.com/Piebald-AI/splitrail), which `gg` was compared with. **No real
log from any of them was available when they were written** — their tests use synthetic fixtures built
from the described format, with expected totals worked out by hand. They are labelled **experimental**:
the notes (`gg notes`, the Notes tab) carry a warning whenever their data is in the totals, and
`gg sources` says so. If you use one of these tools, comparing `gg`'s numbers with the tool's own is
the best check; a sanitised sample log in an issue would let us verify and drop the label.

Common conventions: tokens are normalised like everywhere else (uncached input, cache read/write,
output including reasoning). A field the tool does not record is **n/a**, never `0`. The tool's own
reported cost is not used (prices come from the inspectable price table; unknown models are unpriced).
Data location overrides: `GEMINI_HOME`, `QWEN_HOME`, `PI_HOME`, `OPENCODE_HOME`, `KILO_HOME`, `DSH_HOME`.

### Gemini CLI

**Data:** `~/.gemini/tmp/<project>/chats/*.json` (one session document) and `*.jsonl` (one message per
line, or `{"$set":{"messages":[…]}}` blocks; a later recording of the same message id replaces the earlier).

**Semantics (Google's definitions, not disjoint):** `tokens.input` is the whole prompt *including*
`tokens.cached`; `tokens.output` *excludes* `tokens.thoughts`; `tokens.tool` is tool-use prompt;
`total = input + tool + thoughts + output`. Normalised: Input = (input − cached) + tool, CacheRead =
cached, Output = output + thoughts, Reasoning = thoughts. Gemini CLI records no cache writes.
Projects appear as `gemini:<id>` (Gemini names them by a hash, not a path). For `.jsonl` sessions the
session id is the file name.

### Qwen Code

**Data:** `~/.qwen/projects/<project>/chats/*.jsonl`. Assistant records carry Gemini-style
`usageMetadata` (`promptTokenCount` includes `cachedContentTokenCount`; thoughts are billed as output)
and are normalised like Gemini CLI. The older `~/.qwen/tmp/*/chats/*.json` format is not read.

### Pi Agent

**Data:** `~/.pi/agent/sessions/<project>/*.jsonl`: `session` header (id, cwd), `model_change`
entries and `message` entries with `usage {input, output, cacheRead, cacheWrite}` (disjoint, like
Anthropic). Models are named `provider/model`, which price lookup strips.

### Cline, Roo Code, Kilo Code, Zoo Code (VS Code extensions)

**Data:** `<editor>/User/globalStorage/<extension id>/tasks/<task>/ui_messages.json` for editors Code,
Code - Insiders, Cursor, Windsurf, VSCodium, Positron, Antigravity and VS Code Server, on Linux
(`~/.config/<editor>`), macOS (`~/Library/Application Support/<editor>`) and Windows
(`%APPDATA%\<editor>`). Extension ids: `saoudrizwan.claude-dev`, `rooveterinaryinc.roo-cline`,
`kilocode.kilo-code`, `zoocodeorganization.zoo-code`.

**Semantics:** each `api_req_started` message carries `tokensIn` (uncached), `tokensOut`,
`cacheWrites`, `cacheReads`. Requests still in flight (no token fields), cancelled ones and ones flagged
`usage_missing` are skipped. The model comes from `task_metadata.json` (Cline, by request time) or the
last `<model>…</model>` in `api_conversation_history.json` (Roo, Kilo, Zoo — so a mid-task model
switch is attributed to the last model). **No working directory is recorded**, so projects show as
`(unknown)`. These files change in place, so a changed task is re-read in full.

### OpenCode and Kilo CLI

**Data:** `$XDG_DATA_HOME` or `~/.local/share` + `/opencode/opencode.db` (SQLite, current) and
`/opencode/storage/message/<session>/<id>.json` (legacy); Kilo CLI uses `/kilo/kilo.db`. The database
is opened **read-only**; its `-wal` file counts as part of the file for caching.

**Semantics:** assistant `message.data` JSON carries `tokens {input, output, reasoning, cache:{read,write}}`
(also per `step-finish` part, which is used when the message-level block is empty). `input` excludes
cache; OpenCode computes `output` as outputTokens − reasoning and bills reasoning at the output rate, so
reasoning is **added to output** here (and kept as the Reasoning subset). Projects come from
`session → project.worktree`. If this assumption about `output` is wrong for your version, totals for
reasoning models are off by the reasoning tokens — compare with OpenCode's own stats.

### DeepSeek Harness

**Data:** `$DSH_HOME` or `~/.dsh` + `/sessions/<workspace>/<session>/session.jsonl.zstd` (or
`session.v4.jsonl.zstd`), a zstd-compressed JSONL event log. `assistant/message` events carry
`usage {inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, reasoningTokens}` and the model in
`data.message.source.model`; forked sessions repeat the parent's messages under the same ids, which are
counted once. `reasoningTokens` is assumed to be a **subset** of `outputTokens` (as in DeepSeek's API);
not verified for the harness.

## Not supported, and why

| Tool | Why `gg` does not read it |
|---|---|
| **Grok** (`~/.grok/sessions`) | Per-turn token usage is in `updates.jsonl` with **no timestamp or model of its own**; attributing it to days and models needs the session summary date and a heuristic split across messages. That would be an estimate, not a measurement. |
| **GitHub Copilot CLI** (`~/.copilot/session-state`) | Only output tokens are exact; **input and cache tokens are estimated** from text length and a session-end metrics summary. Mixing measured and estimated numbers would break the "nothing unmeasured" rule. |
| **GitHub Copilot in VS Code** (`workspaceStorage/*/chatSessions`) | Chat sessions do not record token usage reliably; any figure would be a tokenizer estimate. |
| **Antigravity CLI**, **Piebald** | SQLite databases with schemas that vary by version and no real sample or documentation to verify against; a guessed parser would risk silently wrong totals. |

Contributions with a sanitised sample log are welcome for any of these.

## Data that cannot be reliably derived

- **Your actual bill.** Logs contain tokens, not invoices, discounts, credits or plan limits.
- **Usage outside local logs:** web chat UIs, other machines, deleted/rotated logs.
- **Tools listed under "Not supported"** above.
- **Per-request tier/latency/batch pricing:** logs do not reliably say which price tier applied.
- **Exact cache-TTL pricing** when a log omits the TTL breakdown.
- **Project identity beyond the working directory** (two repos in the same folder look the same;
  one repo opened from two paths looks like two projects).
