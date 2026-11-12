"""Renders the README banner and the GitHub social preview from the traced
mark (mark.svg) and the app's typeface (Geist). Run from the repo root:

    python design/logo/render.py

Needs Pillow, and apps/web/node_modules (npm install) for the Geist fonts.
"""
import math
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[2]
OUT = Path(__file__).resolve().parent
FONTS = ROOT / 'apps/web/node_modules/@fontsource'
INK, PAPER, MUTED, FAINT = (10, 10, 10), (250, 250, 250), (150, 150, 150), (38, 38, 38)


def cubic(p0, p1, p2, p3, n=240):
    pts = []
    for i in range(1, n + 1):
        u = i / n
        m = 1 - u
        pts.append((m**3 * p0[0] + 3 * m * m * u * p1[0] + 3 * m * u * u * p2[0] + u**3 * p3[0],
                    m**3 * p0[1] + 3 * m * m * u * p1[1] + 3 * m * u * u * p2[1] + u**3 * p3[1]))
    return pts


def corner(start, end, centre, n=24):
    a0 = math.atan2(start[1] - centre[1], start[0] - centre[0])
    a1 = math.atan2(end[1] - centre[1], end[0] - centre[0])
    if a1 < a0:
        a1 += 2 * math.pi
    r = math.dist(start, centre)
    return [(centre[0] + r * math.cos(a0 + (a1 - a0) * i / n), centre[1] + r * math.sin(a0 + (a1 - a0) * i / n)) for i in range(1, n + 1)]


# One piece in its own 144-unit square (same numbers as mark.svg).
PIECE = [(0, 0), (16.6, 0)] + cubic((16.6, 0), (16.6, 63.4), (43.9, 127.4), (144, 127.4)) + [(144, 144), (4, 144)]
PIECE += corner((4, 144), (0, 140), (4, 140))
MARK = [[(151.8 + x, y) for x, y in PIECE], [(144 - x, 300.7 - y) for x, y in PIECE]]
MARK_W, MARK_H = 295.8, 300.7


def draw_mark(img, x, y, height, colour, ss=4):
    """Draws the mark with its top-left at (x, y), anti-aliased by supersampling."""
    k = height / MARK_H
    w, h = math.ceil(MARK_W * k) + 2, math.ceil(height) + 2
    layer = Image.new('L', (w * ss, h * ss), 0)
    d = ImageDraw.Draw(layer)
    for poly in MARK:
        d.polygon([(px * k * ss, py * k * ss) for px, py in poly], fill=255)
    mask = layer.resize((w, h), Image.LANCZOS)
    img.paste(Image.new('RGB', (w, h), colour), (round(x), round(y)), mask)


def font(family, weight, size):
    return ImageFont.truetype(str(FONTS / family / 'files' / f'{family}-latin-{weight}-normal.woff'), size)


def banner(path, W, H, mark_h, title_px, tag_px, foot_px, centred=False):
    S = 2  # render at 2x
    img = Image.new('RGB', (W * S, H * S), INK)
    d = ImageDraw.Draw(img)
    # Faint dotted grid, like the app's empty workspace.
    step = 28 * S
    for gx in range(step // 2, W * S, step):
        for gy in range(step // 2, H * S, step):
            d.ellipse((gx - S, gy - S, gx + S, gy + S), fill=FAINT)
    title = font('geist-sans', 600, title_px * S)
    tag = font('geist-sans', 400, tag_px * S)
    foot = font('geist-mono', 400, foot_px * S)
    mh = mark_h * S
    mw = mh * MARK_W / MARK_H
    gap = 40 * S
    tw = max(d.textlength('webssh', font=title), d.textlength('The SSH client that lives in your browser.', font=tag) + 4 * S)
    if centred:
        x0 = (W * S - (mw + gap + tw)) / 2
    else:
        x0 = 96 * S
    cy = H * S / 2 - (24 * S if centred else 18 * S)
    draw_mark(img, x0, cy - mh / 2, mh, PAPER)
    tx = x0 + mw + gap
    asc, desc = title.getmetrics()
    d.text((tx, cy - (asc + desc) / 2 - 6 * S), 'webssh', font=title, fill=PAPER)
    d.text((tx + 4 * S, cy + (asc + desc) / 2 - 2 * S), 'The SSH client that lives in your browser.', font=tag, fill=MUTED)
    line = 'self-hosted  ·  end-to-end encrypted vault  ·  teams  ·  sftp  ·  forwarding'
    lw = d.textlength(line, font=foot)
    d.text(((W * S - lw) / 2 if centred else tx + 4 * S, H * S - (58 if centred else 64) * S), line, font=foot, fill=(110, 110, 110))
    img = img.resize((W, H), Image.LANCZOS) if path.name.startswith('social') else img
    img.save(path, optimize=True)
    print('wrote', path.relative_to(ROOT), img.size)


if __name__ == '__main__':
    banner(OUT / 'banner.png', 1280, 360, mark_h=150, title_px=88, tag_px=26, foot_px=16)
    banner(OUT / 'social-preview.png', 1280, 640, mark_h=200, title_px=112, tag_px=32, foot_px=20, centred=True)
