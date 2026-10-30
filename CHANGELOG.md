# Changelog

All notable changes to `gg` are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/). Dates are the dates of the release tags.

## [Unreleased]

### Security

- The installers need `cosign` to verify the release signature and stop without it. `GG_ALLOW_UNVERIFIED=1` installs with
  the checksum check only.

### Changed

- The installers are published as release assets, and the README links to `releases/latest/download/`, not to the
  `main` branch. The installer scripts are listed in the signed `checksums.txt`.
- Releases are drafts until the archives are attested, and only then are they published.
- The release tools are pinned: GoReleaser v2.19.0 and cosign v2.6.5. Dependabot checks the GitHub Actions and Go modules
  weekly, and CI runs `govulncheck` (v1.9.0).
- Documentation corrections: the third-party dependencies, the fuzz coverage, the collision odds, and the performance
  figures that no benchmark in the repository measures.

### Added

- `SECURITY.md`, issue templates for bugs and provider support, and a pull request template.

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
