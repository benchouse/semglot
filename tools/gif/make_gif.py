#!/usr/bin/env python3
"""Regenerate the semglot announcement GIF.

Renders N SVG frames, rasterises each with inkscape, assembles with ImageMagick.
Palette and mark geometry are taken from logo.svg: ink #111111, oxblood #A00000.

Usage:  python3 tools/gif/make_gif.py [outfile.gif]
"""
import os
import shutil
import subprocess
import sys

W, H = 800, 450
BG = "#FBF9F3"
INK = "#111111"
OXBLOOD = "#A00000"
MUTED = "#6B6560"
SERIF = "Georgia, 'Liberation Serif', serif"

TITLE = "Nine Semantic Layer Translations"
SOURCES = ["dbt", "Ossie"]
TARGETS = [
    "Snowflake Cortex",
    "Snowflake semantic view",
    "supersimple",
    "nao / yaml",
    "nao / context rules",
    "Databricks metric view",
    "Lightdash",
    "Ossie",
]
FOOTNOTE = "dbt and Ossie read and write"
FOOTER = "open source"
URL = "benchouse.ai"

CX, CY = 400.0, 232.0          # logo centre
LOGO_SCALE = 1.45              # 64-unit viewBox -> ~93px
TRACK_Y = CY
SRC_X = 170.0                  # right edge of the source boxes
TGT_X = 544.0                  # left edge of the target list
SRC_GAP = 62.0                 # vertical gap between source boxes
TGT_SPAN = 34.0                # vertical gap between targets
HIDE_R = 50.0                  # radius of the mark; balls vanish inside it
FRAMES = 40
DELAY_CS = 5                   # centiseconds per frame

# One ball per target. Sources alternate, so dbt and Ossie fire in turn.
# The jitter breaks the metronome without making it look random.
JITTER = (0.00, 0.06, -0.04, 0.03, -0.02, 0.05, -0.05, 0.02)

MARK = """
  <g stroke="{ink}" fill="none">
    <circle cx="32" cy="32" r="26" stroke-width="2"/>
  </g>
  <g stroke="{ink}" stroke-width="1.6" stroke-linecap="round">
    <path d="M 32,32 32.039294,6.459416"/>
    <path d="M 32,32 54.001408,19.31812"/>
    <path d="M 32,32 54.237173,44.81941"/>
    <path d="m 32,32 0.03929,25.383408"/>
    <path d="M 32,32 9.8807096,44.839057"/>
    <path d="M 32,32 10.07718,19.337767"/>
  </g>
  <g fill="{ink}">
{arrows}
  </g>
  <circle cx="32" cy="32" r="6.9" fill="{ox}"/>
""".format(
    ink=INK,
    ox=OXBLOOD,
    arrows="\n".join(
        '    <path transform="rotate({a},32,32) translate(0.07778543,0.51856955)"'
        ' d="m 25.44089,55.627859 5.797038,1.942661 c 2.508604,-0.681001'
        " 4.990304,-1.012257 7.525811,-2.043003 -2.089753,0.32478 -5.74154,0.216387"
        " -6.014807,-5.954991 l -1.593503,0.03274 c -0.08691,6.4027 -4.102174,6.440593"
        ' -5.714539,6.014777 z"/>'.format(a=a)
        for a in (0, 60, 120, 180, 240, 300)
    ),
)


def source_xy(j: int) -> tuple:
    n = len(SOURCES)
    return SRC_X, TRACK_Y + (j - (n - 1) / 2) * SRC_GAP


def target_xy(j: int) -> tuple:
    n = len(TARGETS)
    return TGT_X, TRACK_Y - (n - 1) / 2 * TGT_SPAN + j * TGT_SPAN


def frame_svg(i: int) -> str:
    phase = i / FRAMES
    n = len(TARGETS)
    dots = []
    for k in range(n):
        p = (phase + k / n + JITTER[k % len(JITTER)]) % 1.0
        sx, sy = source_xy(k % len(SOURCES))
        tx, ty = target_xy(k)

        # first half converges on the mark, second half fans out to a target
        if p < 0.5:
            t = p / 0.5
            x, y = sx + (CX - sx) * t, sy + (CY - sy) * t
        else:
            t = (p - 0.5) / 0.5
            x, y = CX + (tx - CX) * t, CY + (ty - CY) * t

        if (x - CX) ** 2 + (y - CY) ** 2 < HIDE_R**2:
            continue

        # fade in leaving the source, fade out arriving at the target
        op = min(1.0, p / 0.10, (1.0 - p) / 0.10)
        dots.append(
            f'<circle cx="{x:.1f}" cy="{y:.1f}" r="4" '
            f'fill="{OXBLOOD}" opacity="{max(0.0, op):.2f}"/>'
        )

    # source boxes, vertically centred on the track
    boxes = []
    n = len(SOURCES)
    for j, s in enumerate(SOURCES):
        by = TRACK_Y + (j - (n - 1) / 2) * 62
        boxes.append(
            f'<rect x="70" y="{by-21:.0f}" width="96" height="42" rx="6" '
            f'fill="none" stroke="{INK}" stroke-width="1.1"/>'
            f'<text x="118" y="{by+7:.0f}" font-family="{SERIF}" font-size="21" '
            f'fill="{INK}" text-anchor="middle">{s}</text>'
        )

    # target list
    items = []
    n = len(TARGETS)
    span = 34
    top = TRACK_Y - (n - 1) / 2 * span
    for j, t in enumerate(TARGETS):
        items.append(
            f'<text x="552" y="{top + j*span + 5:.0f}" font-family="{SERIF}" '
            f'font-size="15.5" fill="{INK}">{t}</text>'
        )

    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">
<rect width="{W}" height="{H}" fill="{BG}"/>
<text x="{W/2}" y="52" font-family="{SERIF}" font-size="27" font-weight="bold"
      fill="{INK}" text-anchor="middle">{TITLE}</text>
{''.join(boxes)}
{''.join(dots)}
<g transform="translate({CX - 32*LOGO_SCALE:.2f},{CY - 32*LOGO_SCALE:.2f}) scale({LOGO_SCALE})">
{MARK}
</g>
<text x="{CX}" y="{CY + 32*LOGO_SCALE + 26:.0f}" font-family="{SERIF}" font-size="13"
      fill="{INK}" text-anchor="middle" letter-spacing="3.5">SEMGLOT</text>
{''.join(items)}
<text x="{W/2}" y="410" font-family="{SERIF}" font-size="12.5" fill="{MUTED}"
      text-anchor="middle" font-style="italic">{FOOTNOTE}</text>
<text x="{W/2}" y="429" font-family="{SERIF}" font-size="12.5" fill="{MUTED}"
      text-anchor="middle">{FOOTER}</text>
<text x="{W/2}" y="446" font-family="{SERIF}" font-size="13" fill="{INK}"
      text-anchor="middle">{URL}</text>
</svg>"""


def main() -> int:
    out = sys.argv[1] if len(sys.argv) > 1 else "semglot-ossie.gif"
    tmp = "/tmp/semglot-gif"
    shutil.rmtree(tmp, ignore_errors=True)
    os.makedirs(tmp)

    for i in range(FRAMES):
        with open(f"{tmp}/f{i:03d}.svg", "w") as fh:
            fh.write(frame_svg(i))

    subprocess.run(
        ["inkscape", "--export-type=png", f"--export-width={W}",
         *[f"{tmp}/f{i:03d}.svg" for i in range(FRAMES)]],
        check=True, capture_output=True,
    )
    subprocess.run(
        ["convert", "-delay", str(DELAY_CS), "-loop", "0",
         f"{tmp}/f%03d.png[0-{FRAMES-1}]", "-layers", "Optimize", out],
        check=True,
    )
    print(f"wrote {out} ({os.path.getsize(out)/1024:.0f} KB, {FRAMES} frames)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
