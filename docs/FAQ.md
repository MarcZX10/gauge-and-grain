# FAQ and troubleshooting

## "It says no usage" / the tool I use is missing

Run **`gg sources`**. It lists every supported tool, whether its data was found, where it looked, and how many files
it found. If a tool keeps its data somewhere unusual, point to it with the matching variable (`CLAUDE_CONFIG_DIR`,
`CODEX_HOME`, `GEMINI_HOME`, `QWEN_HOME`, `PI_HOME`, `OPENCODE_HOME`, `KILO_HOME`, `DSH_HOME`; see
[PROVIDERS.md](PROVIDERS.md)). Also check the **period**: the default is the last 30 days, and a filter chip in the
header may be narrowing the view (`c` clears every filter).

Tools that are not supported at all (Grok, Copilot, Antigravity, Piebald, Cursor, Aider…) will never appear;
[why](PROVIDERS.md#not-supported-and-why).

## The cost is not what I was billed

Expected. It is the **API-equivalent** cost: your tokens at public list prices. Subscriptions (Claude Pro/Max, ChatGPT
plans, free tiers, credits) and enterprise discounts are priced differently, and logs contain tokens, not invoices.
Use the number to compare models, projects and weeks, not to reconcile an invoice. See
[METRICS.md](METRICS.md#estimated-cost).

## A model shows `unpriced`, and the total says `≥ $…`

`gg` has no price for that model, and it will not guess one from a similar name. Its tokens are counted; its cost is
left out, so the total is a lower bound. Add the price yourself in a `prices.json` ([PRICING.md](PRICING.md)); list the
affected models with `gg prices --unpriced`. If it is a public model, a pull request adding it (with the source URL
and date) is welcome.

## A tool is labelled "experimental". Can I trust the numbers?

Treat them as unverified. Those parsers were built from the documented format of each tool and tested with synthetic
logs, because no real logs were available. Compare `gg`'s figure with the tool's own statistics; if they differ, please
open an issue with a **sanitised** sample so the label can be dropped or the parser fixed.

## The numbers differ from the tool's own stats

Possible reasons: the tool counts differently (for example whether reasoning tokens are part of output — `gg` always
normalises, see [METRICS.md](METRICS.md#how-each-tool-maps-onto-it)); `gg` de-duplicates repeated records that a simpler
counter would add up; the period boundaries differ (`gg` uses whole local days); or the tool has deleted older logs.
`gg export --by events` shows every counted call so you can check a specific session.

## The first run is slow

It reads every log once (a big Codex history can be tens of gigabytes). The dashboard and the report show progress and
you can cancel with `Esc` in the dashboard (or `Ctrl-C` in the report): finished files stay cached and the next run resumes. After that only changed
files are read, and a live session is resumed from where it stopped. A warm start takes about two seconds per million
calls. `--no-cache` ignores the cache; `gg cache clear` empties it.

## The dashboard is slow, or shows garbage

Filtering, changing the period and drilling in are in-memory and take milliseconds, even on hundreds of thousands of
calls. If drawing is wrong:

- make sure the terminal is **UTF-8** and at least **40×8**; try `--ascii` for a font without box/block glyphs;
- `NO_COLOR=1` or `--no-color` disables colour;
- on **Windows**, use Windows Terminal (the old console host has limited Unicode and mouse support);
- over **SSH/tmux**, mouse reporting must be passed through; the keyboard works everywhere.

## Why do the numbers change when I re-run?

Your tools append to their logs while you work, and recent usage appears on the next scan. Press `r` in the dashboard
to rescan. A cancelled scan is marked **PARTIAL** until you rescan.

## Why is the same model listed twice, like `gpt-5.4 (codex)` and `gpt-5.4 (opencode)`?

Two different tools reported the same model name, and they are different rows (different sessions, different
coverage). The provider is added only when the name would otherwise be ambiguous.

## Can I reset or inspect what it stored?

`gg sources` prints the cache directory; it holds only derived data ([PRIVACY.md](PRIVACY.md#what-is-written)). Delete
it with `gg cache clear`.

## Does it work offline? Does it phone home?

It works fully offline and never makes a network request ([PRIVACY.md](PRIVACY.md)).

## How do I update, or uninstall?

Update: run the install command again. Uninstall: delete the `gg` binary (`~/.local/bin/gg`, or
`%LOCALAPPDATA%\gg\bin\gg.exe`) and, if you like, its cache directory.
