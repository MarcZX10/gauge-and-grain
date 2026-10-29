#!/usr/bin/env bash
# Regenerates docs/img/*.png from the REAL TUI running on synthetic demo data.
#   scripts/screenshots.sh            (needs: go, tmux, python3 + Pillow, chromium)
# Nothing here uses anyone's real logs: see scripts/demodata.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=docs/img
TMP=$(mktemp -d)
trap 'tmux kill-server 2>/dev/null || true; rm -rf "$TMP" /tmp/gg-demo' EXIT
mkdir -p "$OUT"

go build -o "$TMP/gg" ./cmd/gg
DEMO=/tmp/gg-demo   # a stable, neutral path: it shows up in the Notes screenshot
go run ./scripts/demodata -out "$DEMO" >/dev/null
eval "$(go run ./scripts/demodata -out "$DEMO" -env)"
export TERM=xterm-256color LANG=C.UTF-8
unset NO_COLOR

# shot NAME COLS ROWS TITLE "gg args" [keys...]
shot() {
  local name=$1 cols=$2 rows=$3 title=$4 args=$5; shift 5
  tmux kill-server 2>/dev/null || true; sleep 0.5
  tmux new-session -d -s shot -x "$cols" -y "$rows" "$TMP/gg $args; sleep 30"
  for _ in $(seq 1 60); do
    sleep 0.5
    tmux capture-pane -t shot -p | grep -qE "Overview|Ovw" && break
  done
  sleep 0.7
  for k in "$@"; do tmux send-keys -t shot $k; sleep 0.7; done
  sleep 0.5
  tmux capture-pane -t shot -e -p > "$TMP/$name.ansi"
  python3 scripts/ansi2png.py "$TMP/$name.ansi" "$OUT/$name.png" --cols "$cols" --rows "$rows" --title "$title" --scale 1.5
  echo "  $OUT/$name.png"
}

shot overview   130 40 "gg"                   "tui --days 30"
shot daily      130 40 "gg · Daily"           "tui --days 30" 2 "Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down Down"
shot models     130 22 "gg · Models"          "tui --days 30" 4 Down
shot notes      120 44 "gg · Notes"           "tui --days 30" n
shot period     110 26 "gg · choose a period" "tui --days 30" t Down Down Down Down Down
shot compact    110 12 "gg --compact"         "tui --days 30 --compact"
shot narrow      62 34 "gg · narrow terminal" "tui --days 30"

# the plain command-line report, coloured
FORCE_COLOR=1 COLUMNS=100 "$TMP/gg" report --compact --days 30 > "$TMP/cli-compact.ansi"
python3 scripts/ansi2png.py "$TMP/cli-compact.ansi" "$OUT/cli-compact.png" --cols 100 --rows 5 --title "gg report --compact" --scale 1.5
echo "  $OUT/cli-compact.png"
