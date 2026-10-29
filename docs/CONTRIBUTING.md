# Contributing

Thanks for helping. The most valuable contributions are **real (sanitised) sample logs** from tools whose parsers are
still experimental, **prices** with a source, and **new tools** with fixtures.

## Build and test

```sh
make build        # ./gg
make test         # everything, including a 1M-record large-history test (~15 s)
make test-short   # skips the large-history and latency tests
make cross        # compile for linux/darwin/windows × amd64/arm64
go vet ./... && gofmt -l .   # CI fails on either
```

Go 1.26+ (the version in `go.mod`). The only third-party code is Bubble Tea (event loop), `go-runewidth`, `go-isatty`, `x/term`, `x/sys`,
`modernc.org/sqlite` (pure-Go, no CGO) and `klauspost/compress/zstd`. CI runs the tests on Linux, macOS and Windows.

## Layout

```
cmd/gg              CLI entry point: flags, commands, progress meter
internal/model      the provider-neutral Event
internal/providers  one adapter per tool (Detect, Files, NewParser), fixtures-driven tests
internal/scan       parallel scan, incremental cache, de-duplication, cancellation
internal/pricing    embedded + user prices (prices.json), model matching, cost
internal/agg        index of day×provider×model×project×session cells, filters, summaries
internal/render     report sections, panels/charts, notes, JSON/CSV export (shared with the TUI)
internal/tui        the dashboard
scripts/            installers, demodata (synthetic history), screenshots, ansi2png
testdata/           fixtures per provider
docs/               this documentation
```

How the pieces fit, the cache contract and the memory profile: [ARCHITECTURE.md](ARCHITECTURE.md).

## Adding or fixing a tool

The checklist is in [ARCHITECTURE.md](ARCHITECTURE.md#adding-a-provider). The rules that keep the tool trustworthy:

1. **Emit only what the log says.** Set an event's `Has` flag only for fields the source really reports; an absent
   field is *n/a*, never `0`.
2. **Give every event a stable `Key`** so duplicates (streamed chunks, resumed or archived copies) are counted once.
3. **Treat the log as hostile.** Skip and count malformed records, clamp counters, never panic on any bytes (add the
   parser to `FuzzParsersNeverPanic`).
4. **Bump `ParserVersion()`** whenever the output could change; it invalidates the cache.
5. **Fixtures with hand-derived totals.** Put (sanitised) logs in `testdata/<tool>/` and assert totals you worked out by
   hand, including a duplicate, a malformed line and an absurd counter. Prefer **real** logs. Synthetic fixtures must
   say they are synthetic, and the provider must implement `Experimental()` until a real log verifies it.
6. **Do a negative control**: break the behaviour on purpose and confirm a test fails.
7. **Prices need a source URL and a date** in `internal/pricing/prices.json`. Unknown models stay unpriced.
8. Document the data location, assumptions and limitations in [PROVIDERS.md](PROVIDERS.md).

### Promoting an experimental tool

Send a sanitised real log (remove prompts and paths; keep the structure and counters) in an issue or pull request. If
the parser's totals match the tool's own statistics, replace the synthetic fixture with the real one, remove
`Experimental()`, and update the tables in the README and PROVIDERS.md.

## Screenshots

The images in the README are generated, not drawn: the **real TUI** runs on **synthetic demo data**, is captured through
`tmux`, converted to HTML and rendered to PNG by headless Chromium.

```sh
scripts/screenshots.sh          # needs go, tmux, python3 + Pillow, chromium; ~40 s
go run ./scripts/demodata -out /tmp/gg-demo        # just the demo data
eval "$(go run ./scripts/demodata -out /tmp/gg-demo -env)" && gg    # explore it yourself
```

`scripts/demodata` is seeded (`-seed`), so the data is reproducible; it writes plausible Claude Code, Codex, Gemini CLI
and OpenCode histories (with an unpriced model on purpose, so the notes have something to say). Set `CHROMIUM=/path`
if Chromium is not on `PATH`. Regenerate the images whenever the UI changes, and look at them before committing.

## Releasing

Releases are built from tags by `.github/workflows/release.yml` (GoReleaser):

```sh
git tag -a v0.6.0 -m "gg v0.6.0" && git push origin v0.6.0
```

It runs the tests, then publishes `gg_<os>_<arch>` archives and `checksums.txt`, which the installers use. Check the
installer afterwards: `curl -fsSL …/scripts/install.sh | sh`.

## Style

Match the surrounding code: comments explain *why* (assumptions, units, invariants), names are plain, errors are
reported not hidden. Commits describe the change and its reason. Keep claims in the UI and docs to what is measured.

When `go.mod` changes, regenerate the third-party notices that every release archive ships: `make notices`
(CI fails if `THIRD_PARTY_NOTICES.md` is out of date).
