#!/usr/bin/env python3
"""Generates web/src/textures.css: tileable, mineral-inspired SVG textures used as CSS masks (so the theme's own colours
paint them). Every motif is a clean, regular construction (no scribbled noise): lattices, ripples, rulings. Deterministic:
re-run after editing, never edit textures.css by hand.

    python3 web/tools/gen-textures.py
"""
import math, random, urllib.parse, os

def svg(w, h, body):
    # shared presentation attributes live on one group: every shape then only carries what differs
    s = (f"<svg xmlns='http://www.w3.org/2000/svg' width='{w:.1f}' height='{h:.1f}' viewBox='0 0 {w:.1f} {h:.1f}'>"
         f"<g fill='none' stroke='white' stroke-linecap='round' stroke-linejoin='round'>{body}</g></svg>")
    enc = urllib.parse.quote(s, safe="/:=,.'() ")
    return f'url("data:image/svg+xml;utf8,{enc}")', (w, h)

def path(d, sw=0.7, op=1.0, extra=""):
    return f"<path d='{d}' stroke-width='{sw:g}' stroke-opacity='{op:.2g}'/>"

def circ(x, y, r, fill=False, sw=0.7, op=1.0):
    if fill:
        return f"<circle cx='{x:.1f}' cy='{y:.1f}' r='{r:.1f}' fill='white' fill-opacity='{op:.2g}' stroke='none'/>"
    return f"<circle cx='{x:.1f}' cy='{y:.1f}' r='{r:.1f}' stroke-width='{sw:g}' stroke-opacity='{op:.2g}'/>"

out = {}
def h(*a):   # deterministic pseudo-random in [0,1) from integers — identical on both sides of a tile seam
    return random.Random(sum((v + 7) * (k * 2654435761 % 9973 + 13) for k, v in enumerate(a))).random()

def poly(pts, close=True, sw=0.6, op=1.0):
    return path("M" + "L".join(f"{x:.1f},{y:.1f}" for x, y in pts) + ("Z" if close else ""), sw, op)

def hexpts(cx, cy, R, rot=-30):
    return [(cx + R * math.cos(math.radians(60 * k + rot)), cy + R * math.sin(math.radians(60 * k + rot))) for k in range(6)]

def hexlattice(R, cols, rows):
    cw = math.sqrt(3) * R
    for r in range(-1, rows + 1):
        for c in range(-1, cols + 1):
            yield c % cols, r % rows, c * cw + (cw / 2 if r % 2 else 0) + cw / 2, r * 1.5 * R + R

# ── emerald / beryl: the hexagonal prism cross-section, each cell a touch stronger or fainter; a few cells show
#    the inner growth-zone hexagon
def beryl():
    R = 18.0; cols, rows = 8, 10; W, H = math.sqrt(3) * R * cols, 1.5 * R * rows; b = []
    for c, r, cx, cy in hexlattice(R, cols, rows):
        v = h(1, c, r)
        b.append(poly(hexpts(cx, cy, R), True, 0.6, [0.2, 0.3, 0.45, 0.6, 0.8][int(v * 5)]))
        if h(2, c, r) < 0.22:
            b.append(poly(hexpts(cx, cy, R * 0.52), True, 0.5, 0.3))
    return svg(W, H, "".join(b))
out["hex"] = beryl()

# ── tanzanite: a cut stone's diamond facets; alternate cells carry an inner table and kite spokes
def facets():
    p = 40.0; n = 2; W = H = p * n; b = []
    for i in range(n):
        for j in range(n):
            x, y = i * p, j * p
            cx, cy = x + p / 2, y + p / 2
            b.append(poly([(cx, y), (x + p, cy), (cx, y + p), (x, cy)], True, 0.6, 0.55))
            if (i + j) % 2 == 0:
                q = p / 4
                b.append(poly([(cx, cy - q), (cx + q, cy), (cx, cy + q), (cx - q, cy)], True, 0.5, 0.4))
                for ax, ay, bx, by in ((cx, cy - q, cx, y), (cx + q, cy, x + p, cy), (cx, cy + q, cx, y + p), (cx - q, cy, x, cy)):
                    b.append(path(f"M{ax:.1f},{ay:.1f}L{bx:.1f},{by:.1f}", 0.4, 0.3))
    return svg(W, H, "".join(b))
out["facets"] = facets()

# ── cobaltite: cubic habit — a hexagonal lattice with three spokes per cell reads as a field of isometric cubes
def cubes():
    R = 20.0; cols, rows = 6, 8; W, H = math.sqrt(3) * R * cols, 1.5 * R * rows; b = []
    for c, r, cx, cy in hexlattice(R, cols, rows):
        pts = hexpts(cx, cy, R)
        b.append(poly(pts, True, 0.6, 0.6))
        for k in (1, 3, 5):
            b.append(path(f"M{cx:.1f},{cy:.1f}L{pts[k][0]:.1f},{pts[k][1]:.1f}", 0.5, 0.4))
    return svg(W, H, "".join(b))
out["cubes"] = cubes()

# ── amber: slow resin flow — evenly spaced undulating lines drifting out of phase, with a few trapped bubbles
def flow():
    W = H = 160.0; b = []
    for i in range(8):
        base = 10 + i * 20; ph = i * 0.55
        pts = [(x, base + 4.5 * math.sin(2 * math.pi * x / W + ph)) for x in range(0, 161, 8)]
        b.append(poly(pts, False, 0.7, 0.55 if i % 2 == 0 else 0.28))
    for x, y, r in ((36, 40, 3.2), (118, 20, 2.4), (84, 100, 3.6), (140, 126, 2.6), (22, 140, 2.2)):
        b.append(circ(x, y, r, False, 0.6, 0.85))
        b.append(path(f"M{x - r * 0.55:.1f},{y - r * 0.1:.1f}A{r * 0.6:.1f},{r * 0.6:.1f} 0 0 1 {x - r * 0.05:.1f},{y - r * 0.55:.1f}", 0.4, 0.8))
    return svg(W, H, "".join(b))
out["flow"] = flow()

# ── amethyst: a geode lined with points — offset rows of pointed prisms overlapping like feathers
def crystal():
    pitch, w, hgt = 24.0, 18.0, 36.0; cols, rows = 4, 4; W, H = pitch * cols, hgt * rows; b = []
    for r in range(rows):
        for c in range(-1, cols + 1):
            cx = c * pitch + pitch / 2 + (pitch / 2 if r % 2 else 0); top = r * hgt
            op = [0.45, 0.7, 0.55][(c + r) % 3]
            b.append(path(f"M{cx - w / 2:.1f},{top + hgt:.1f}L{cx - w / 2:.1f},{top + 14:.1f}L{cx:.1f},{top:.1f}L{cx + w / 2:.1f},{top + 14:.1f}L{cx + w / 2:.1f},{top + hgt:.1f}", 0.6, op))
            b.append(path(f"M{cx:.1f},{top:.1f}L{cx:.1f},{top + hgt:.1f}", 0.4, 0.28))
            b.append(path(f"M{cx - w / 2:.1f},{top + 14:.1f}L{cx:.1f},{top + 22:.1f}L{cx + w / 2:.1f},{top + 14:.1f}", 0.4, 0.28))
    return svg(W, H, "".join(b))
out["crystal"] = crystal()

# ── opal: close-packed silica spheres in alternating layers, a few with a coloured core
def spheres():
    sp = 18.0; rh = sp * math.sqrt(3) / 2; cols, rows = 12, 14; W, H = sp * cols, rh * rows; b = []
    for r in range(-1, rows + 1):
        for c in range(-1, cols + 1):
            x = c * sp + (sp / 2 if r % 2 else 0) + sp / 2; y = r * rh + rh
            b.append(circ(x, y, 7.2, False, 0.6, 0.62 if r % 2 else 0.36))
            if h(3, c % cols, r % rows) < 0.18:
                b.append(circ(x, y, 2.6, True, 0.2, 0.7))
    return svg(W, H, "".join(b))
out["spheres"] = spheres()

# ── aquamarine: growth striations — ruled lines along the crystal's length in a repeating rhythm, each broken a different way
def striae():
    W = H = 120.0; b = []
    rhythm = [(0.9, 0.7, "92 28"), (0.4, 0.4, "40 20"), (0.6, 0.55, "120 0"), (0.4, 0.3, "20 20"), (0.7, 0.6, "55 5")]
    for i in range(15):
        sw, op, dash = rhythm[i % len(rhythm)]
        x = 4 + i * 8
        b.append(f"<path d='M{x},0L{x},{H:.0f}' stroke-width='{sw}' stroke-opacity='{op}' stroke-dasharray='{dash}' stroke-dashoffset='{(i * 17) % 40}'/>")
    return svg(W, H, "".join(b))
out["striae"] = striae()

# ── moonstone: feldspar lamellae — diagonal rulings, every fourth one heavier, crossed by a faint twin set
def lamellae():
    W = H = 96.0; b = []
    for i in range(16):
        c = i * 6.0
        sw, op = (1.0, 0.7) if i % 4 == 0 else ((0.6, 0.4) if i % 2 == 0 else (0.5, 0.25))
        for off in (-W, 0, W):
            b.append(path(f"M{c + off:.1f},0L{c + off + W:.1f},{H:.0f}", sw, op))
    for i in range(2):
        c = i * 48.0 + 6
        for off in (-W, 0, W):
            b.append(path(f"M{c + off + W:.1f},0L{c + off:.1f},{H:.0f}", 0.5, 0.2))
    return svg(W, H, "".join(b))
out["lamellae"] = lamellae()

# ── obsidian: conchoidal fracture — concentric shell ripples that fan out and fade, each arc turned a little further
def fracture():
    W = H = 128.0; b = []
    for cx, cy, rot in ((32.0, 32.0, 0), (96.0, 96.0, 90)):
        for i in range(1, 7):
            r = i * 4.6; span = 210 - i * 16; a0 = rot + i * 24
            x1, y1 = cx + r * math.cos(math.radians(a0)), cy + r * math.sin(math.radians(a0))
            x2, y2 = cx + r * math.cos(math.radians(a0 + span)), cy + r * math.sin(math.radians(a0 + span))
            b.append(path(f"M{x1:.1f},{y1:.1f}A{r:.1f},{r:.1f} 0 {1 if span > 180 else 0} 1 {x2:.1f},{y2:.1f}", 0.7, max(0.3, 0.95 - i * 0.1)))
        b.append(circ(cx, cy, 1.3, True, 0.2, 0.9))
    return svg(W, H, "".join(b))
out["fracture"] = fracture()

# ── the plain grid, for anyone who prefers it
def grid():
    s = 19.0; W = H = s * 12
    b = "".join(path(f"M{i * s:.1f},0L{i * s:.1f},{H}", 0.5, 1) + path(f"M0,{i * s:.1f}L{W},{i * s:.1f}", 0.5, 1) for i in range(12))
    return svg(W, H, b)
out["grid"] = grid()

lines = ["/* GENERATED by web/tools/gen-textures.py — do not edit by hand. Mineral-inspired SVG tiles used as CSS masks. */", ":root {"]
for name, (url, (w, h)) in out.items():
    lines.append(f"  --tex-{name}: {url};")
    lines.append(f"  --tex-{name}-size: {w:.2f}px {h:.2f}px;")
lines.append("}")
dest = os.path.join(os.path.dirname(__file__), "..", "src", "textures.css")
open(dest, "w").write("\n".join(lines) + "\n")
print(dest, {k: round(len(v[0]) / 1024, 1) for k, v in out.items()}, "KB")
