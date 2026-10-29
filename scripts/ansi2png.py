#!/usr/bin/env python3
"""Render ANSI terminal output (e.g. `tmux capture-pane -e -p`) as a PNG.

    ansi2png.py capture.ansi out.png --cols 120 --rows 36 --title "gg tui"

Standard library only, plus a headless Chromium for the rasterising (set
CHROMIUM=/path/to/chromium if it is not on PATH). Used by scripts/screenshots.sh.
"""
import argparse, html, os, re, shutil, subprocess, sys, tempfile

BG, FG = "#1e1e2e", "#cdd6f4"
BASE16 = ["#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de",
          "#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8"]

def xterm(n):
    if n < 16:
        return BASE16[n]
    if n < 232:
        n -= 16
        v = lambda c: 0 if c == 0 else 55 + 40 * c
        return "#%02x%02x%02x" % (v(n // 36), v(n // 6 % 6), v(n % 6))
    g = 8 + 10 * (n - 232)
    return "#%02x%02x%02x" % (g, g, g)

SGR = re.compile(r"\x1b\[([0-9;]*)m")

def parse(text, cols):
    rows = []
    for line in text.split("\n"):
        st = dict(fg=None, bg=None, bold=False, dim=False, ul=False, rev=False)
        cells, i = [], 0
        while i < len(line):
            m = SGR.match(line, i)
            if m:
                p = [int(x) if x else 0 for x in m.group(1).split(";")] or [0]
                j = 0
                while j < len(p):
                    c = p[j]
                    if c == 0: st.update(fg=None, bg=None, bold=False, dim=False, ul=False, rev=False)
                    elif c == 1: st["bold"] = True
                    elif c == 2: st["dim"] = True
                    elif c == 4: st["ul"] = True
                    elif c == 7: st["rev"] = True
                    elif c == 22: st.update(bold=False, dim=False)
                    elif c == 24: st["ul"] = False
                    elif c == 27: st["rev"] = False
                    elif c == 39: st["fg"] = None
                    elif c == 49: st["bg"] = None
                    elif 30 <= c <= 37: st["fg"] = c - 30
                    elif 90 <= c <= 97: st["fg"] = c - 90 + 8
                    elif 40 <= c <= 47: st["bg"] = c - 40
                    elif c in (38, 48) and j + 2 < len(p) and p[j + 1] == 5:
                        st["fg" if c == 38 else "bg"] = p[j + 2]; j += 2
                    j += 1
                i = m.end()
                continue
            if line[i] == "\x1b":  # any other escape: skip to its final byte
                m2 = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]").match(line, i)
                i = m2.end() if m2 else i + 1
                continue
            cells.append((line[i], dict(st)))
            i += 1
        rows.append(cells[:cols] + [(" ", dict(fg=None, bg=None, bold=False, dim=False, ul=False, rev=False))] * max(0, cols - len(cells)))
    return rows

def colour(st):
    fg = xterm(st["fg"]) if st["fg"] is not None else FG
    bg = xterm(st["bg"]) if st["bg"] is not None else None
    if st["rev"]:
        fg, bg = (bg or BG), (fg if st["fg"] is not None or True else FG)
    return fg, bg

def to_html(rows, title, cols):
    out = []
    for cells in rows:
        runs, cur, buf = [], None, []
        for ch, st in cells:
            key = (st["fg"], st["bg"], st["bold"], st["dim"], st["ul"], st["rev"])
            if key != cur and buf:
                runs.append((cur_st, "".join(buf))); buf = []
            cur, cur_st = key, st
            buf.append(ch)
        if buf:
            runs.append((cur_st, "".join(buf)))
        line = []
        for st, txt in runs:
            fg, bg = colour(st)
            css = "color:%s;" % fg
            if bg: css += "background:%s;" % bg
            if st["bold"]: css += "font-weight:700;"
            if st["dim"]: css += "opacity:.62;"
            if st["ul"]: css += "text-decoration:underline;"
            line.append('<span style="%s">%s</span>' % (css, html.escape(txt)))
        out.append("".join(line))
    body = "\n".join(out)
    return f"""<!doctype html><meta charset=utf-8><style>
html,body{{margin:0;background:transparent}}
.wrap{{display:inline-block;margin:28px}}
.win{{background:{BG};border-radius:12px;box-shadow:0 12px 40px rgba(0,0,0,.45),0 0 0 1px rgba(255,255,255,.07);overflow:hidden}}
.bar{{height:34px;background:#181825;display:flex;align-items:center;padding:0 14px;gap:8px;font:13px 'DejaVu Sans',sans-serif;color:#7f849c}}
.dot{{width:12px;height:12px;border-radius:50%}}
pre{{margin:0;padding:14px 18px 18px;font:15px/19px 'DejaVu Sans Mono',monospace;color:{FG};font-variant-ligatures:none;letter-spacing:0;white-space:pre;width:{cols}ch}}
</style><div class=wrap><div class=win><div class=bar><i class=dot style=background:#f38ba8></i><i class=dot style=background:#f9e2af></i><i class=dot style=background:#a6e3a1></i><span style=margin-left:10px>{html.escape(title)}</span></div><pre>{body}</pre></div></div>"""

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src"); ap.add_argument("dst")
    ap.add_argument("--cols", type=int, required=True)
    ap.add_argument("--rows", type=int)
    ap.add_argument("--title", default="gg")
    ap.add_argument("--scale", type=float, default=2.0)
    a = ap.parse_args()
    text = open(a.src, encoding="utf-8").read().rstrip("\n")
    rows = parse(text, a.cols)
    if a.rows:
        rows = (rows + [[(" ", dict(fg=None, bg=None, bold=False, dim=False, ul=False, rev=False))] * a.cols] * a.rows)[: a.rows]
    h = to_html(rows, a.title, a.cols)
    chromium = os.environ.get("CHROMIUM") or shutil.which("chromium") or shutil.which("chromium-browser") or shutil.which("google-chrome")
    if not chromium:
        sys.exit("ansi2png: no chromium found (set CHROMIUM=...)")
    with tempfile.TemporaryDirectory() as d:
        f = os.path.join(d, "t.html"); open(f, "w", encoding="utf-8").write(h)
        w = int(a.cols * 9.1 + 140); ht = int(len(rows) * 19 + 140)
        raw = os.path.join(d, "raw.png")
        subprocess.run([chromium, "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars", "--default-background-color=00000000",
                        "--force-device-scale-factor=%s" % a.scale, "--window-size=%d,%d" % (w, ht), "--screenshot=" + raw, "file://" + f],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        from PIL import Image
        im = Image.open(raw).convert("RGBA")
        im.crop(im.getchannel("A").point(lambda v: 255 if v > 0 else 0).getbbox()).save(a.dst, optimize=True)

if __name__ == "__main__":
    main()
