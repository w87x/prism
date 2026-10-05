#!/usr/bin/env python3
"""Generates web/src/textures.css: tileable, mineral-inspired SVG textures used as CSS masks (so the theme's own colours
paint them). Deterministic (seeded): re-run after editing, never edit textures.css by hand.

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

# ── beryl / emerald: a hexagonal lattice (the crystal's prism cross-section), each cell drawn with its own strength, plus
#    the cracks and tiny inclusions ("jardin") that real emeralds are known for
def beryl():
    R = 18.0; cw = math.sqrt(3) * R; cols, rows = 8, 10   # rows even → seamless
    W, H = cw * cols, 1.5 * R * rows
    rnd = random.Random(7); b = []
    def hexpath(cx, cy):
        pts = [(cx + R * math.cos(math.radians(60 * k - 30)), cy + R * math.sin(math.radians(60 * k - 30))) for k in range(6)]
        return "M" + "L".join(f"{x:.1f},{y:.1f}" for x, y in pts) + "Z"
    for r in range(-1, rows + 1):
        for c in range(-1, cols + 1):
            cx = c * cw + (cw / 2 if r % 2 else 0) + cw / 2; cy = r * 1.5 * R + R
            op = random.Random(1000 + (c % cols) * 31 + (r % rows)).choice([0.18, 0.28, 0.4, 0.55, 0.8])   # same strength on both sides of the seam
            b.append(path(hexpath(cx, cy), 0.6, op))
    for _ in range(7):                       # cracks
        x, y = rnd.uniform(30, W - 30), rnd.uniform(30, H - 30); d = f"M{x:.1f},{y:.1f}"
        a = rnd.uniform(0, 6.28)
        for _ in range(rnd.randint(4, 7)):
            a += rnd.uniform(-0.8, 0.8); x += math.cos(a) * rnd.uniform(8, 18); y += math.sin(a) * rnd.uniform(8, 18); d += f"L{x:.1f},{y:.1f}"
        b.append(path(d, 0.9, 0.9))
    for _ in range(18):                      # inclusions
        b.append(circ(rnd.uniform(6, W - 6), rnd.uniform(6, H - 6), rnd.uniform(0.7, 1.7), True, 0.2, 0.9))
    return svg(W, H, "".join(b))
out["hex"] = beryl()

# ── tanzanite: sparse triangular facets, like light catching a cut stone
def facets():
    s = 60.0; hh = s * math.sqrt(3) / 2; cols, rows = 4, 4
    W, H = s * cols, hh * rows; rnd = random.Random(11); b = []
    for r in range(-1, rows + 1):
        for c in range(-1, cols + 1):
            x0 = c * s + (s / 2 if r % 2 else 0); y0 = r * hh
            for k, (dx, dy) in enumerate(((s, 0), (s / 2, hh), (-s / 2, hh))):
                kr = random.Random(2000 + (c % cols) * 101 + (r % rows) * 7 + k)
                if kr.random() < 0.55:
                    b.append(path(f"M{x0:.1f},{y0:.1f}L{x0+dx:.1f},{y0+dy:.1f}", 0.6, kr.choice([0.3, 0.5, 0.8])))
    return svg(W, H, "".join(b))
out["facets"] = facets()

# ── cobaltite: cubic cleavage — broken stair-steps of near-square blocks
def cubes():
    W = H = 240.0; rnd = random.Random(23); b = []
    xs = sorted(rnd.sample(range(10, 230, 6), 11)); ys = sorted(rnd.sample(range(10, 230, 6), 11))
    for y in ys:
        x = rnd.uniform(0, 30)
        while x < W:
            seg = rnd.uniform(18, 70)
            if rnd.random() < 0.6:
                b.append(path(f"M{x:.1f},{y}L{min(W, x + seg):.1f},{y}", 0.7, rnd.choice([0.35, 0.6, 0.9])))
            x += seg + rnd.uniform(6, 40)
    for x in xs:
        y = rnd.uniform(0, 30)
        while y < H:
            seg = rnd.uniform(18, 70)
            if rnd.random() < 0.6:
                b.append(path(f"M{x},{y:.1f}L{x},{min(H, y + seg):.1f}", 0.7, rnd.choice([0.35, 0.6, 0.9])))
            y += seg + rnd.uniform(6, 40)
    return svg(W, H, "".join(b))
out["cubes"] = cubes()

# ── amber: the slow flow lines of resin and the little bubbles trapped in it
def flow():
    W = H = 240.0; rnd = random.Random(31); b = []
    for i in range(11):
        base = 10 + i * 22 + rnd.uniform(-5, 5); k = rnd.choice([1, 2, 3]); amp = rnd.uniform(3, 11); ph = rnd.uniform(0, 6.28)
        pts = [(x, base + amp * math.sin(2 * math.pi * k * x / W + ph)) for x in range(0, 241, 8)]
        b.append(path("M" + "L".join(f"{x:.1f},{y:.1f}" for x, y in pts), rnd.choice([0.5, 0.7, 1.0]), rnd.choice([0.25, 0.45, 0.7])))
    for _ in range(16):
        b.append(circ(rnd.uniform(8, W - 8), rnd.uniform(8, H - 8), rnd.uniform(1.2, 3.4), False, 0.6, 0.8))
    return svg(W, H, "".join(b))
out["flow"] = flow()

# ── amethyst: a geode — rows of pointed prisms
def crystal():
    W, H = 240.0, 240.0; rnd = random.Random(41); b = []
    cw = 30.0; cols = 8; rows = 3; rh = H / rows
    for r in range(rows):
        for c in range(cols):
            x = c * cw + cw / 2; top = r * rh + rnd.uniform(4, 18); bot = (r + 1) * rh - 2
            w = cw * rnd.uniform(0.32, 0.46)
            b.append(path(f"M{x - w:.1f},{bot:.1f}L{x - w:.1f},{top + w:.1f}L{x:.1f},{top:.1f}L{x + w:.1f},{top + w:.1f}L{x + w:.1f},{bot:.1f}", 0.7, rnd.choice([0.3, 0.55, 0.85])))
            b.append(path(f"M{x:.1f},{top:.1f}L{x:.1f},{bot:.1f}", 0.4, 0.4))
    return svg(W, H, "".join(b))
out["crystal"] = crystal()

# ── opal: close-packed silica spheres (the actual microstructure behind its play of colour)
def spheres():
    sp = 20.0; rh = sp * math.sqrt(3) / 2; cols, rows = 12, 14
    W, H = sp * cols, rh * rows; rnd = random.Random(53); b = []
    for r in range(-1, rows + 1):
        for c in range(-1, cols + 1):
            x = c * sp + (sp / 2 if r % 2 else 0) + sp / 2; y = r * rh + rh
            kr = random.Random(3000 + (c % cols) * 53 + (r % rows))
            b.append(circ(x, y, kr.uniform(3.5, 8.8), False, 0.6, kr.choice([0.2, 0.35, 0.6, 0.9])))
    return svg(W, H, "".join(b))
out["spheres"] = spheres()

# ── aquamarine: growth striations running along the crystal's length
def striae():
    W = H = 240.0; rnd = random.Random(61); b = []
    for _ in range(46):
        x = rnd.uniform(4, W - 4); y = rnd.uniform(-20, H - 30); ln = rnd.uniform(30, 150)
        b.append(path(f"M{x:.1f},{max(0, y):.1f}L{x + rnd.uniform(-2, 2):.1f},{min(H, y + ln):.1f}", rnd.choice([0.4, 0.6, 0.9]), rnd.choice([0.25, 0.45, 0.75])))
    return svg(W, H, "".join(b))
out["striae"] = striae()

# ── moonstone: fine parallel lamellae of intergrown feldspar
def lamellae():
    W = H = 240.0; rnd = random.Random(71); b = []
    cs = sorted(rnd.uniform(0, 240) for _ in range(30))
    for c in cs:
        for off in (-240, 0, 240):
            b.append(path(f"M{c + off:.1f},0L{c + off - 240:.1f},{240}", rnd.choice([0.4, 0.6, 1.0]), rnd.choice([0.2, 0.4, 0.7])))
    # the same lines continued across the other edge keep the tile seamless
    return svg(W, H, "".join(b))
out["lamellae"] = lamellae()

# ── obsidian: conchoidal fracture — shell-like ripples fanning out from a few impact points
def fracture():
    W = H = 240.0; rnd = random.Random(83); b = []
    for (cx, cy) in ((75, 80), (170, 150), (60, 190)):
        a0 = rnd.uniform(0, 6.28)
        for i in range(1, 8):
            r = i * 8.5 + rnd.uniform(-1, 1)
            if cx - r < 2 or cx + r > W - 2 or cy - r < 2 or cy + r > H - 2:
                continue
            span = rnd.uniform(1.4, 2.6); aa = a0 + rnd.uniform(-0.3, 0.3)
            x1, y1 = cx + r * math.cos(aa), cy + r * math.sin(aa); x2, y2 = cx + r * math.cos(aa + span), cy + r * math.sin(aa + span)
            b.append(path(f"M{x1:.1f},{y1:.1f}A{r:.1f},{r:.1f} 0 0 1 {x2:.1f},{y2:.1f}", rnd.choice([0.5, 0.8]), 0.25 + 0.1 * (8 - i)))
    for _ in range(5):
        x, y = rnd.uniform(20, 220), rnd.uniform(20, 220); a = rnd.uniform(0, 6.28); d = f"M{x:.1f},{y:.1f}"
        for _ in range(4):
            a += rnd.uniform(-0.5, 0.5); x += math.cos(a) * 11; y += math.sin(a) * 11; d += f"L{x:.1f},{y:.1f}"
        b.append(path(d, 0.5, 0.5))
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
