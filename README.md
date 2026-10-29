# gauge-and-grain (`gg`)

**Where does your AI usage go?** `gg` reads the usage logs that your AI coding assistants already
write to disk and turns them into one honest picture: tokens, API-equivalent cost, sessions,
models, projects and trends — across providers, in a compact report or an interactive TUI.

- **Local only.** It reads files on your machine. It never uploads prompts, transcripts, source
  code or usage data, and it makes no network requests at all.
- **Honest numbers.** Unknown models show as *unpriced*, fields a source does not report show as
  *n/a* — never as a silent `0`. Estimates are labelled as estimates.
- **No double counting.** Streamed chunks, resumed sessions, archived copies and Codex turn-end
  re-broadcasts are counted once.
- **Fast on big histories.** An incremental cache means only new or changed data is re-read.

| Provider | Status |
|---|---|
| Claude Code | supported |
| OpenAI Codex (CLI / IDE) | supported |
| Gemini CLI | detected, **not parsed yet** (no verified sample of its format) — see [docs/PROVIDERS.md](docs/PROVIDERS.md) |

## Install

**Linux / macOS**

```sh
curl -fsSL https://raw.githubusercontent.com/MarcZX10/gauge-and-grain/main/scripts/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/MarcZX10/gauge-and-grain/main/scripts/install.ps1 | iex
```

**With Go 1.24+ (any platform)**

```sh
go install github.com/MarcZX10/gauge-and-grain/cmd/gg@latest
```

**Update:** run the same command again. The installers verify the release's SHA-256 checksum before
replacing anything and install to `~/.local/bin` (`%LOCALAPPDATA%\gg\bin` on Windows); override with
`GG_INSTALL_DIR`, or pin a version with `GG_VERSION=v0.1.0`.

> Release binaries (Linux, macOS, Windows × amd64, arm64) are built by the `release` workflow whenever a
> `v*` tag is pushed; see the [Releases](https://github.com/MarcZX10/gauge-and-grain/releases) page.

## Use

```sh
gg                       # TUI on a terminal; compact report when piped
gg report                # compact report, last 30 days
gg report --days 7       # last week
gg report --from 2026-10-01 --to 2026-10-15
gg report --provider codex --model gpt-5
gg report --project my-app --daily
gg tui --all             # interactive, whole history
gg export --format json  > usage.json
gg export --format csv --by model > models.csv     # --by day|provider|model|project|session|events
gg sources               # what was found on this machine
gg prices --unpriced     # price table, sources, dates; models in your history with no price
```

### The compact report

```
$ gg report --from 2026-09-01 --to 2026-10-07
gauge-and-grain  custom range  (2026-09-01 → 2026-10-07)

Tokens    5.8K          no usage in the previous period to compare with
Est. cost ≥ $0.01       partial: 200 tokens unpriced (1 model)
Sessions  4             12 API calls (de-duplicated)
Sources   Claude Code · OpenAI Codex · Gemini CLI (not found)

TOKEN MIX  what each kind of token means
  input                    1.2K     20%  █▌░░░░░░  new, uncached prompt tokens
  cache read               3.0K     52%  ████▏░░░  prompt tokens reused from cache (cheap)
  cache write              1.0K     17%  █▍░░░░░░  stored into cache; priced above input
  output                    592     10%  ▊░░░░░░░  generated tokens
    of which thinking        84  reasoning tokens: a subset of output, not added again
  cache hit share of input-side tokens: 58%

MODELS  unpriced = no price known; its tokens are counted but cost is excluded
  model                                    tokens   share   est. cost   sess
  gpt-5.4                                    2.4K     41%     $0.0039      1  █████▋░░░░░░░░
  claude-sonnet-5-5                          2.3K     39%     $0.0067      1  █████▍░░░░░░░░
  mystery-model-9             unpriced        200    3.5%    unpriced      1  ▍░░░░░░░░░░░░░

READING THIS HONESTLY
  • Cost is an estimate of what these tokens would cost at public API list prices — not your
    subscription bill. Plans like Claude Pro/Max or ChatGPT bill differently, so it will not
    match an invoice.
  • Unpriced: mystery-model-9. Their tokens are included above, their cost is not. …
  • Ignored 6 malformed record(s), clamped 3 implausible counter(s), skipped 0 oversized
    line(s). Totals exclude them.
```

(Real output from the repository's test fixtures, abbreviated — they deliberately contain an unpriced model and malformed lines.)

### The TUI

| Key | Action |
|---|---|
| `Tab` / `Shift+Tab`, `←` `→`, `1`–`6` | Overview · Daily · Providers · Models · Projects · Sessions |
| `↑` `↓` / `j` `k`, `PgUp` `PgDn`, `g` `G` | move / scroll |
| `Enter` | drill into the selected day, provider, model or project (filters the whole view) |
| `Esc` / `Backspace` | undo the last drill-down → clear search → back to Overview |
| `d` | range: 7 days → 30 days → 90 days → all time |
| `p` | cycle provider filter |
| `/` | search model, project or session text |
| `s` | sort by tokens or by estimated cost |
| `c` | clear every filter |
| `r` | rescan (cached files are not re-read) |
| `?` | help · `q` / `Ctrl-C` quit |

The first scan of a large history shows a progress bar. **`Esc` cancels cleanly**: files already
parsed stay cached, you see what was read so far (clearly marked partial), and the next run resumes.
Below 40×8 the TUI asks for a bigger terminal instead of drawing garbage; narrower terminals drop
columns and shorten labels.

### Filters (report, tui, export)

| Flag | Meaning |
|---|---|
| `--days N` | last N days including today (default 30) |
| `--from D --to D` | date range `YYYY-MM-DD`, local time, `--to` inclusive |
| `--all` | whole history |
| `--provider ID` | `claude`, `codex` (repeat or comma-separate) |
| `--model TEXT` | model name contains TEXT |
| `--project TEXT` | project path contains TEXT |

Output: `--no-color` (also `NO_COLOR`, `TERM=dumb`, or piping), `--ascii`, `--no-cache`,
`--prices FILE`.

## What the numbers mean

- **Tokens** = uncached input + cache read + cache write + output, defined identically for every
  provider. Reasoning ("thinking") tokens are shown separately but are a *subset of output*, so they
  are not added twice.
- **Cache read** are prompt tokens served from cache (cheap). **Cache write** are tokens stored into
  the cache (Anthropic bills these above normal input; OpenAI does not bill them separately).
- **Est. cost** is *API-equivalent*: tokens × public list prices. It is useful for comparing
  models and projects, but it **is not your bill** — subscriptions (Claude Pro/Max, ChatGPT plans)
  are priced differently. `≥ $X` means some tokens are unpriced, so `$X` is a lower bound.
- **vs previous period** compares with the immediately preceding window of the same length
  (only when the range is bounded).
- **Fair comparisons.** Providers report different fields. Where a source never reports a field
  (e.g. cache writes) the report says so and exports contain an empty cell / `null`, not `0`.

## Privacy

`gg` opens only the session files listed in [docs/PROVIDERS.md](docs/PROVIDERS.md), extracts token
counters, model names, timestamps, session ids and working directories, and discards everything else
(prompts and responses are never retained or written to the cache). Nothing leaves your machine —
the binary makes no network calls. The cache holds only the extracted counters.

Be aware that **exports contain project paths and session ids**; review them before sharing.
CSV cells that start with `= + - @` are prefixed with `'` so a hostile folder name cannot become a
spreadsheet formula.

## Configuration

| What | Where |
|---|---|
| Price overrides | `<config dir>/gauge-and-grain/prices.json` or `--prices FILE` ([docs/PRICING.md](docs/PRICING.md)) |
| Cache | `<cache dir>/gauge-and-grain/` — safe to delete (`gg cache clear`) |
| Claude Code data | `$CLAUDE_CONFIG_DIR/projects`, else `~/.config/claude/projects` and `~/.claude/projects` |
| Codex data | `$CODEX_HOME/sessions` and `archived_sessions`, else `~/.codex/…` |
| Test/dev overrides | `GG_CONFIG_DIR`, `GG_CACHE_DIR` |

`<config dir>` / `<cache dir>` are the OS defaults (`~/.config`, `~/.cache`; `~/Library/…` on macOS;
`%AppData%` / `%LocalAppData%` on Windows).

## Known limitations

- **Gemini CLI is not parsed** in this release (detected only); its usage is *not* in the totals, and
  the report says so. Other tools (Copilot CLI, Cursor, Aider, OpenCode…) are not supported yet.
- Cost covers **standard-tier list prices only** — batch/flex/priority tiers, long-context surcharges
  and subscription pricing are not modelled. Models without a price are *unpriced*, never guessed.
- Claude Code **cache write** price is derived from documented multipliers (1.25× 5-minute,
  2× 1-hour). Some cache-read prices are derived as 0.1× input where the vendor table does not list
  one (`gg prices` marks them).
- Codex logs do not say how `cache_write_input_tokens` relates to `input_tokens`; `gg` assumes it is
  part of input (as cached tokens are). The field was always `0` in the logs this was developed on.
- If a tool changes its log format, affected records are skipped and counted under
  "malformed" in the report rather than guessed.
- `gg` was developed and run on Linux. The Windows and macOS builds cross-compile cleanly and the
  CI workflow runs the tests on all three systems, but they have had no real-world use yet, and the
  Windows/macOS log locations are taken from each tool's documentation, not verified on those systems.
- Memory is ~180 bytes per API call retained in memory (1M calls ≈ 180 MB); see
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Development

```sh
make build        # ./gg
make test         # unit, fixture, CLI and 1M-record large-history tests
make test-short   # skips the large-history test
make cross        # compile for linux/darwin/windows × amd64/arm64
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how to add a provider.

## Licence

MIT — see [LICENSE](LICENSE).
