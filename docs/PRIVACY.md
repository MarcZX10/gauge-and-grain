# Privacy and security

`gg` is built to be safe to point at logs full of your own prompts and code.

## Nothing leaves your machine

- `gg` reads local files and prints to your terminal or to stdout. It does not upload prompts, transcripts, code,
  usage figures or telemetry, and has no analytics, update check or "phone home".
- The binary contains **no HTTP client and no TLS**. You can check:

  ```sh
  go list -deps ./cmd/gg | grep -E '^(net/http|crypto/tls)$'      # prints nothing
  ```

  The `net`-family packages in the build are `net/url` (parsing the file URI used to open a SQLite database read-only),
  and `net` with `net/netip`, which come in through a UUID library inside the SQLite driver (type definitions for hardware
  addresses). gg's code never dials, listens or resolves a name, so the program makes no network request.
- The binary also links `os/exec`: Bubble Tea, the terminal colour library and the SQLite driver's C runtime import it.
  gg's own code does not run a program. (`scripts/notices` runs `go list`, and tests run the built binary.)
- Only the **installers** use the network (`scripts/install.sh`, `scripts/install.ps1`), to download a release from
  GitHub. They verify its signature with `cosign` and its SHA-256 checksum before installing anything. Without `cosign`
  they stop, unless you set `GG_ALLOW_UNVERIFIED=1` (see [Verify a download](#verify-a-download)). Prices are embedded
  in the binary, so everything works offline.

## What is read

Only the usage logs listed in [PROVIDERS.md](PROVIDERS.md), and nothing else on disk. Each line of a log is read into
memory briefly to find the usage record; prompts and replies in the same file are skipped over. What is **kept** from a
record is:

- provider, model name, timestamp, session id, working-directory path;
- token counters (input, cache read/write, output, reasoning);
- a de-duplication key (a message/response id).

Message text, tool output, file contents and credentials are never retained, written to the cache, or exported. Two
parsers open a second file next to a log to learn the model (Cline's `task_metadata.json`; Roo, Kilo and Zoo's
`api_conversation_history.json`, where only the `<model>…</model>` tag is read). Both are read only if they are regular
files of at most 64 MB, and changing one of them invalidates the cached result of the task it belongs to.

## What is written

`gg` writes only these:

- **The cache directory** (`gg sources` prints the path): per source file, the kept fields above plus the parser state
  needed to resume (for example Codex's current model and cumulative counters, or Pi's working directory). The directory
  is readable only by you (`0700`, files `0600`). It is derived data: `gg cache clear` removes the files `gg` wrote.
  Entries whose log has disappeared are removed on the next scan. Writes are atomic, through temporary `gg-*.tmp` files
  in the same directory.
- **`tui.json`** in the config directory (`<config dir>/gauge-and-grain/`, or `GG_CONFIG_DIR`), written when the dashboard
  exits: the sort, view, period preset and compact mode. It holds no paths, no names and no usage figures. Delete it to
  forget them.
- **`gg-<view>-<time>.csv`** in the folder you started `gg` from, only when you press `e` in the dashboard. It is the same
  CSV as `gg export` (see below), and it can contain project paths and session ids. It is created exclusively with mode
  `0644` before your umask, so other users on the machine may be able to read it.

`gg` never modifies or deletes the logs it reads.

SQLite databases (OpenCode, Kilo CLI) are never written to, and `gg` creates no `-shm`, `-wal` or journal files beside
them. A database with no write-ahead log is opened in immutable mode. When its `-wal` holds recent rows, the database and
its log are copied to a new `gg-sqlite-*` directory in the system temporary directory (at most 1 GB), read there, and that
directory is removed as soon as the database has been read, including after an error. It is not kept until exit. If gg
is killed during that read, the copy can be left behind; delete the `gg-sqlite-*` folders then.

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
  are skipped; sibling model files over 64 MB are ignored; SQLite databases over 8 GB are skipped; a database row over
  8 MB is skipped.
- Only **regular files** are read. A named pipe, a device or a socket named like a log (`x.jsonl`) is ignored, so it
  cannot block a scan. A symbolic link to a regular file is followed; a data directory that is a symbolic link is
  followed too.
- Compressed (zstd) sessions can expand only up to 1 GB; a session still being written, or cut short, keeps what
  decoded.
- Text taken from a log (model names, session ids, working directories) has terminal control characters removed and is
  capped at 1 KB before it is cached, counted or printed, so a log cannot change your terminal (retitle the window, clear
  the screen) or inflate the cache.
- SQLite queries are cancelled after three minutes.
- A corrupt database is reported as an unreadable file, and the rest of the scan continues.
- CSV cells starting with `=`, `+`, `-` or `@` are neutralised so a hostile path cannot execute as a spreadsheet
  formula.
- The text parsers (every log format except the zstd and SQLite readers) are run against arbitrary bytes in a fuzz test
  (`FuzzParsersNeverPanic`) that must never panic. The zstd and SQLite readers are covered by tests with truncated and
  corrupt inputs instead.
- Cache files are written atomically (a private temporary file, then rename); a corrupt or old cache entry is simply
  re-parsed.

## Verify a download

Release archives are built by GitHub Actions from a tag (see `.github/workflows/release.yml`). The workflow itself checks
that the tag points at `main`, runs the tests, signs `checksums.txt` keylessly with the workflow's own identity
(Sigstore), and attests the archives with GitHub build provenance. The `release` environment that holds the publishing
job is protected by a repository setting, not by the code: a required reviewer and a deployment-tag rule. The maintainer
checks that setting before each release ([release checklist](CONTRIBUTING.md#release-checklist)).

A checksum only shows that a file is intact; the signature and the provenance show where it was built. Check both:

```sh
# 1. the checksums were signed by this repository's release workflow
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github.com/MarcZX10/gauge-and-grain/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt

# 2. the archive matches those checksums
sha256sum -c checksums.txt --ignore-missing

# 3. the archive was built by that workflow (needs the GitHub CLI)
gh attestation verify gg_linux_amd64.tar.gz --repo MarcZX10/gauge-and-grain
```

The installers do steps 1 and 2 and stop without `cosign` (or install with only step 2 if you set
`GG_ALLOW_UNVERIFIED=1`). Or skip binaries and build from source:
`go install github.com/MarcZX10/gauge-and-grain/cmd/gg@latest` (requires Go 1.26 or later).

The installer scripts are release assets too, and they are listed in `checksums.txt`, so steps 1 and 2 cover them.
`curl ... | sh` runs a script before any check can happen. To check it first, download `install.sh` (or `install.ps1`)
together with `checksums.txt`, `checksums.txt.pem` and `checksums.txt.sig`, run steps 1 and 2 in that folder, and only
then run the script. Releases before the one that added the scripts do not contain them.

Found a problem? Open an issue — and please sanitise any log you attach.
