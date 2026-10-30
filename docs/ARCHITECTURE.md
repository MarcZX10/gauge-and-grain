# Architecture

**Language: Go.** One static binary per platform (no runtime to install), fast streaming JSON
parsing, trivial cross-compilation to Linux/macOS/Windows on amd64/arm64, and the same ecosystem as
`goccc`. The direct third-party requirements (the `go.mod` entries without `// indirect`) are Bubble Tea (TUI event
loop), `modernc.org/sqlite` (pure-Go SQLite driver, read-only), `klauspost/compress` (zstd), `go-runewidth`, `go-isatty`,
`x/term` and `x/sys`. They bring in indirect modules (Lip Gloss, termenv, google/uuid and others), which `go.mod` and
`THIRD_PARTY_NOTICES.md` list in full.

```
cmd/gg            CLI: flags, commands, progress meter
internal/model    Event: one billed API call, provider-neutral
internal/providers  one adapter per tool: Detect, Files, NewParser
internal/scan     parallel scan, incremental cache, global de-duplication, cancellation
internal/pricing  embedded + user price tables, model matching, cost
internal/agg      filters, groupings, previous-period comparison, coverage
internal/render   report sections, panels/charts, notes, JSON/CSV export (shared by the TUI)
internal/tui      Bubble Tea interface
```

## Data flow

```
files ──► Provider.NewParser ──► []Event per file ──► cache (gob, per file)
                                                         │
all files' events ──► global de-dup (Key) ──► agg.Summarize(filter) ──► render / tui / export
```

### Events and token semantics
`Event` normalises every provider to: **Input** (uncached), **CacheRead**, **CacheWrite**
(+ the 1-hour subset), **Output** (including reasoning), and **Reasoning** (informational subset).
`Has` flags record which optional fields the source actually reported, so exports and reports can say
*n/a* instead of `0`.

### Parsing untrusted logs
- A cheap byte pre-filter skips lines that cannot contain usage before JSON decoding (most of a Codex
  log), so multi-GB histories parse at disk speed.
- Anything that fails to decode or has the wrong shape is **counted as malformed and skipped**, never
  summed. Negative or absurd (> 1e12) counters become 0 and are counted as anomalies.
- Lines over 64 MB are skipped and counted (`oversized`), not buffered.
- A fuzz test (`FuzzParsersNeverPanic`) feeds arbitrary bytes to the line parsers, and to the whole-document parsers
  as one document, of eleven providers: Claude, Codex, Gemini, Qwen, Pi, OpenCode, Kilo CLI, Cline, Roo Code, Kilo Code
  and Zoo Code. DeepSeek (zstd) and the SQLite readers are not fuzzed; they have truncated-archive and corrupt-database
  tests instead.

### Incremental cache
One gob file per source file, keyed by path hash. An entry is reused only if the cache schema and
`Provider.ParserVersion()` match **and** the file's size and mtime are unchanged. Bump
`ParserVersion()` whenever a parser's output could change — that is the cache-invalidation contract.

For **append-only logs** that merely grew (your live session), the entry also stores the byte offset
of the last complete line, fingerprints of the file head and of the bytes just before the offset, and
the parser's resumable state. If the fingerprints still match, only the new tail is parsed; otherwise
the file is re-parsed from the start. A final line without a newline is left unconsumed while the
file is still being written (so a half-written record is never counted or marked malformed), and is
accepted once the file has been idle for two minutes. Cache writes are atomic (temp file + rename), a
corrupt entry is simply re-parsed, and a cancelled scan leaves finished files cached.

### De-duplication
Each event has a `Key` (`claude:<message.id>`, `codex:resp:<response_id>`, …). Keys are
merged within a file (streamed chunks: counters take the max) and **across files** (resumed sessions,
archived copies). The global pass uses a 64-bit hash of the key (collision odds ≈ 7e-7 at 5M events, the birthday bound n²/2⁶⁵),
interns repeated strings and drops the keys to keep memory flat.

### Aggregation index
`agg.NewIndex` folds events once into *cells* — one per (day, provider, model, project, session) — and
prices each cell once (all events in a cell share a model, so they share a price). Every filter, period
change or drill-down is then a pass over the cells, not over millions of events, and no regular
expression or price lookup runs per event. `BenchmarkSummarizeIndexed` (`go test -bench Summarize ./internal/agg`)
times a summary of 300k synthetic events on your machine, and `TestIndexEqualsNaivePerEventSummary` checks the index
against a naive per-event computation for random filters and time zones. Date filters therefore have
day granularity in the chosen time zone.

### Cancellation and progress
`scan.Run(ctx, …)` takes a context; workers stop between and inside files (the reader checks the
context). The CLI shows a one-line meter on stderr when it is a terminal (stdout stays clean for
pipes); the TUI shows a progress bar and `Esc` cancels. A cancelled scan returns what it has,
flagged `Cancelled`, and every view says the totals are partial.

### Source shapes
Most logs are append-only JSONL read line by line (and resumed). Some tools write other shapes, which
providers declare through small optional interfaces: `WholeFiler` (one JSON document per file, e.g.
Gemini sessions and Cline's `ui_messages.json`; re-read in full when it changes), `PathReader` (the
provider opens the file itself — SQLite databases for OpenCode and zstd streams for DeepSeek; the
database's `-wal` sidecar is part of its cache key) and `Experimental` (a reason string; every view
warns while such a provider's data is included).

### Limits
There are **no event caps**: every event is kept so totals are exact. The cost is memory: each call keeps a
176-byte `model.Event` struct (`unsafe.Sizeof`; the strings it refers to are extra, and the total was not measured), and
the warm-start time to decode the cache. Neither is benchmarked over a whole history. If that becomes a problem the next step is pre-aggregating cached entries by day/model/project;
it is not done because it would complicate drill-down filters.

## Adding a provider

1. Create `internal/providers/<name>.go` implementing `Provider`:
   - `Detect()` — say honestly if the directory is missing, empty, or found-but-unparseable.
   - `Files()` — list the log files.
   - `NewParser(path, resume)` — return a `Parser` with `Line`, `Result`, `State`.
     Emit events with a stable `Key`, set `Has` flags only for fields the source really reports,
     use `clamp()` for counters, and count bad records in `ParseStats`.
   - `ParserVersion()` — start at 1; bump on every behavioural change.
2. Register it in `providers.All()`.
3. Add prices for its models in `internal/pricing/prices.json` with a source URL and date.
4. Add **fixtures taken from real (sanitised) logs** to `testdata/<name>/` and tests with
   hand-derived expected totals, including duplicates and malformed lines.
5. Document the data location, assumptions and limitations in [PROVIDERS.md](PROVIDERS.md).

A provider whose format cannot be verified should stay detection-only (see `Gemini`).

## Release

Tag `vX.Y.Z` → `.github/workflows/release.yml` runs the tests and GoReleaser, publishing
`gg_<os>_<arch>.tar.gz` / `.zip` and `checksums.txt`. `scripts/install.sh` and `scripts/install.ps1`
(also release assets) download the latest release, verify the signature and the checksum, then install.

## Notes and the UI

Caveats are data, not decoration: `render.NoteItems` builds a list of `Note{Warn, Text}` from the same
`Ctx` that feeds every view (cost caveat, unpriced models, providers found but unparsed, unreadable
files, partial scans, per-provider field coverage, malformed/duplicate counts, price sources and
dates). The report shows a one-line pointer, `gg notes` / `--notes` prints them, and the TUI has a
dedicated Notes view plus a badge and banner — so a warning can never exist in one place and be
missing in another.

The TUI is a pure function of its model (`View()`), laid out from one `geometry()` that the mouse
handler shares, and `tui_test.go` renders every view at sizes from 40×8 to 220×60 (colour and ASCII)
asserting exact line counts and that no line is wider than the terminal.
