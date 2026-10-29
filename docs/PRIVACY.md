# Privacy and security

`gg` is built to be safe to point at logs full of your own prompts and code.

## Nothing leaves your machine

- `gg` reads local files and prints to your terminal or to stdout. It does not upload prompts, transcripts, code,
  usage figures or telemetry, and has no analytics, update check or "phone home".
- The binary contains **no HTTP client and no TLS**. You can check:

  ```sh
  go list -deps ./cmd/gg | grep -E '^(net/http|crypto/tls)$'      # prints nothing
  ```

  The only `net`-family imports are `net/url` (parsing the file URI used to open a SQLite database read-only) and
  `net` pulled in by a UUID library inside the SQLite driver (type definitions for hardware addresses; nothing is
  dialled).
- Only the **installers** use the network (`scripts/install.sh`, `scripts/install.ps1`), to download a release from
  GitHub; they verify its SHA-256 checksum before installing anything. Prices are embedded in the binary, so
  everything works offline.

## What is read

Only the usage logs listed in [PROVIDERS.md](PROVIDERS.md), and nothing else on disk. Each line of a log is read into
memory briefly to find the usage record; prompts and replies in the same file are skipped over. What is **kept** from a
record is:

- provider, model name, timestamp, session id, working-directory path;
- token counters (input, cache read/write, output, reasoning);
- a de-duplication key (a message/response id).

Message text, tool output, file contents and credentials are never retained, written to the cache, or exported. Two
parsers open a second file next to a log to learn the model (Cline's `task_metadata.json`; Roo, Kilo and Zoo's
`api_conversation_history.json`, where only the `<model>…</model>` tag is read, and only if the file is under 64 MB).

## What is written

`gg` writes to exactly one place: its **cache** directory (`gg sources` prints the path), which holds, per source
file, the kept fields above plus a little parser state needed to resume (for example Codex's current model and
cumulative counters, or Pi's working directory). It is derived data: delete it any time with `gg cache clear`. `gg`
never modifies or deletes the logs it reads. SQLite databases are opened **read-only**.

## What an export contains

[Exports](EXPORT.md) include **working-directory paths and session ids** (and model names). They contain no
prompts or code, but a path can reveal a client or project name — look before you share one. Screenshots in this
repository come from synthetic data (`scripts/demodata`).

## Logs are untrusted input

Local logs can be corrupt, truncated mid-write, or deliberately hostile (a folder named `=HYPERLINK(...)` is a valid
folder name). `gg` is designed so that none of this can crash it or corrupt totals:

- A line that is not valid JSON, has the wrong shape, or an impossible timestamp is **skipped and counted** as
  malformed — never summed. A half-written final line of a live file is left for the next run.
- Negative or absurd counters (over 10¹²) are **clamped to zero and counted**, so one bad record cannot swamp a total.
- Lines over **64 MB** are skipped (counted as oversized) instead of buffered; single-document files over **256 MB**
  are skipped; sibling model files over 64 MB are ignored.
- SQLite databases are opened read-only; a corrupt database or a file that is not zstd is reported as an unreadable
  file, and the rest of the scan continues.
- CSV cells starting with `=`, `+`, `-` or `@` are neutralised so a hostile path cannot execute as a spreadsheet
  formula.
- Every parser is run against arbitrary bytes in a fuzz test (`FuzzParsersNeverPanic`) that must never panic.
- Cache files are written atomically (temp file + rename); a corrupt or old cache entry is simply re-parsed.

## Verifying a release

Release archives are built by GitHub Actions from a tag (see `.github/workflows/release.yml`) and published with a
`checksums.txt`. The installers check it; you can too: `sha256sum -c checksums.txt --ignore-missing`. Or skip binaries
and build from source: `go install github.com/MarcZX10/gauge-and-grain/cmd/gg@latest`.

Found a problem? Open an issue — and please sanitise any log you attach.
