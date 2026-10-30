# Changelog

All notable changes to `gg` are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/). Dates are the dates of the release tags.

## [Unreleased]

## [0.7.0] - 2026-10-30

### Security

- The installers need `cosign` to verify the release signature and stop without it. `GG_ALLOW_UNVERIFIED=1` installs with
  the checksum check only.
- The release is attested before it is published. Dependabot checks the GitHub Actions and Go modules weekly, and CI runs
  `govulncheck`. The build uses Go 1.26.1, which fixes GO-2026-4602 in the standard library `os` package.
- OpenCode databases with a write-ahead log are copied to a temporary directory. Read errors are reported instead of
  producing partial totals, stale copies are removed, and a copy over the size limit is an error, not a truncated copy.
- Symlinked directories inside a log root are followed, so `gg` can read `.jsonl` files outside that root. See
  [PRIVACY](docs/PRIVACY.md).

### Changed

- Price lookup is exact after normalisation. Unlisted variants are no longer priced at their parent's rate, and aliases are
  listed explicitly. Dotted Claude versions resolve, and `claude-sonnet-4-5` and `claude-opus-4-1` are priced. A broken
  price file is an error at load.
- The daily view covers the whole window and includes days without usage. The JSON `by_day` and the day CSV include them.
- JSON export: `partial` is always present (with `partial_reason` when cancelled), `range.timezone` is the IANA name or
  `null`, and `range.utc_offset` is new. CSV output has no `partial` marker.
- The cost change against the previous period is shown only when both periods are complete.
- Events dated before 2015 or more than a day in the future are dropped and counted. The cache schema is 4.
- Parser versions are bumped, so the first run after upgrading reparses the logs.
- `--format` and `--by` given where they do nothing are errors.
- The installers are published as release assets, GoReleaser v2.19.0 and cosign v2.6.5 are pinned, and releases are
  drafts until they are attested.
- Documentation matches the behaviour: privacy, architecture, limitations, metrics, providers, prices and export. The
  release checklist is in [CONTRIBUTING](docs/CONTRIBUTING.md).

### Added

- Codex `local_shell_call` tool calls are counted as `local_shell`.
- `aliases` and a `provider` field in the price data.
- `SECURITY.md`, issue templates for bugs and provider support, a pull request template, and a notice that `gg` is not
  affiliated with the providers whose logs it reads.

### Fixed

- Claude: a call written as two lines, one with `requestId` and one without, is counted once.
- Codex: a tool call whose arguments mention `token_count` keeps its tool use.
- Pi and Gemini: forked entries and the `.json` and `.jsonl` copies of one session are not counted twice. Pi model names
  drop the provider prefix.
- DeepSeek: a negative output count is clamped once.
- The cost change no longer shows a spurious rise against an incomplete previous period.
- CSV export returns write errors, so a full disk no longer leaves a truncated file behind with exit code 0.
- Cache pruning keeps a provider's cache when its logs cannot be found (dead links, unmounted disks).
- An existing cache directory keeps its permissions.
- Dashboard: the Daily tab no longer panics with no data; background refresh keeps the cursor and a day drill; Esc cancels a
  refresh; a newer scan cancels the older one; ASCII panels keep their borders; the cursor stays on screen after a resize;
  going back restores the provider filter; search and the picker take one space; export works only on table tabs; the
  preferences file is written atomically.
- Only visible rows are drawn, so large session lists stay responsive.
- `FORCE_COLOR=0` and `FORCE_COLOR=false` turn colour off. Unpriced lists have a stable order. Providers keep a marker
  without colour.
- A midnight that does not exist (DST gap) no longer includes the excluded day. Session labels are unique within a summary.
- A FIFO in a log directory no longer blocks the scan. A changed side file is reparsed. An unterminated last line is
  counted once it is complete.
- Token counts promote their unit after rounding, and the progress meter shows bytes in byte units.

## [0.6.1] - 2026-10-30

### Changed

- The dashboard keeps table columns stable while a row is selected.

## [0.6.0] - 2026-10-30

### Added

- Tool and skill uses from Claude Code transcripts, in a Tools view; Codex tool calls (function, custom and web search).
- Dashboard command-line filters, a modal picker, quit, export and saved preferences.
- A Daily activity calendar above the chart.

### Changed

- Dashboard auto-refresh is opt-in.
- After a parser upgrade the data is shown at once, and the rest refreshes in the background.
- Drill-down and comparisons are exact.
- Release: licence notices, signed checksums and least-privilege permissions for the release workflow.

### Fixed

- Log ingestion and the cache are hardened against malformed and oversized input.
- Tool uses are counted once, by tool id.

## [0.5.1] - 2026-10-29

### Added

- Reproducible demo data (`scripts/demodata`) and a screenshot pipeline; dashboard screenshots.

### Changed

- Models with the same name from different tools are shown as separate rows.
- The README is shorter, and the detail moved into `docs/`.

## [0.5.0] - 2026-10-29

### Added

- A compact summary (`gg report --compact`, or `m` in the dashboard).

### Changed

- The Daily view highlights the selected or hovered day.

## [0.4.0] - 2026-10-29

### Added

- Experimental parsers for ten more tools: Gemini CLI, Qwen Code, Pi Agent, DeepSeek Harness, OpenCode, Kilo CLI, Cline, Roo Code,
  Kilo Code and Zoo Code. The data they produce is flagged as experimental.

### Changed

- Gemini models are priced. Experimental data is flagged in the notes, the sources and the interface.

## [0.3.0] - 2026-10-29

### Changed

- The dashboard has clearer menus and period selection, and renders from a cache.
- Summaries are computed from a cell index, which makes them much faster.

## [0.2.0] - 2026-10-29

### Added

- `gg notes`: the caveats, as structured notes.
- The interactive dashboard was redesigned: dashboard, details panel, Notes view and mouse support.

## [0.1.1] - 2026-10-29

### Changed

- Versions are printed with a leading `v`.

## [0.1.0] - 2026-10-29

### Added

- The first release: Claude Code and Codex parsers, Gemini detection, aggregation, the compact report, JSON and CSV export,
  the `gg` command with its interactive dashboard, CI, the release workflow and the installers, and the MIT licence.

[Unreleased]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.6.1...HEAD
[0.6.1]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/MarcZX10/gauge-and-grain/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/MarcZX10/gauge-and-grain/releases/tag/v0.1.0
