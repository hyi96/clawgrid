#!/usr/bin/env python3
"""Generate Clawgrid favicon assets without external image dependencies."""

from __future__ import annotations

import argparse
import math
import struct
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_OUT_DIR = ROOT / "web" / "public" / "assets"

BG_TOP = (63, 87, 106)
BG_MID = (28, 44, 56)
BG_BOTTOM = (9, 15, 20)
BG_GLOW = (122, 155, 177)
PANEL = (20, 32, 41)
BORDER = (124, 149, 168)
GRID = (74, 96, 114)
GRID_GLOW = (142, 168, 188)
ROUTE_IDLE = (92, 121, 143)
ROUTE_IDLE_HI = (172, 196, 214)
ROUTE_ACTIVE = (224, 155, 45)
ROUTE_ACTIVE_HI = (252, 224, 163)
ROUTE_SHADOW = (57, 31, 10)
NODE_IDLE = (173, 197, 214)
NODE_ACTIVE = (248, 204, 112)
NODE_CORE = (19, 28, 35)
VIGNETTE = (4, 8, 11)

CENTER = (0.50, 0.50)
NODE_TL = (0.28, 0.28)
NODE_TR = (0.72, 0.28)
NODE_BL = (0.28, 0.72)
NODE_BR = (0.72, 0.72)
GRID_POSITIONS = (0.28, 0.50, 0.72)


def clamp(value: float, lo: float = 0.0, hi: float = 1.0) -> float:
    return max(lo, min(hi, value))


def lerp(a: float, b: float, t: float) -> float:
    return a + (b - a) * t


def mix_rgb(a: tuple[int, int, int], b: tuple[int, int, int], t: float) -> tuple[float, float, float]:
    return (
        lerp(a[0], b[0], t),
        lerp(a[1], b[1], t),
        lerp(a[2], b[2], t),
    )


def overlay(base: tuple[float, float, float], top: tuple[int, int, int], alpha: float) -> tuple[float, float, float]:
    alpha = clamp(alpha)
    return (
        lerp(base[0], top[0], alpha),
        lerp(base[1], top[1], alpha),
        lerp(base[2], top[2], alpha),
    )


def round_rect_sdf(x: float, y: float, half_w: float, half_h: float, radius: float) -> float:
    dx = abs(x - 0.5) - half_w + radius
    dy = abs(y - 0.5) - half_h + radius
    outside = math.hypot(max(dx, 0.0), max(dy, 0.0))
    inside = min(max(dx, dy), 0.0)
    return outside + inside - radius


def circle_sdf(px: float, py: float, cx: float, cy: float, radius: float) -> float:
    return math.hypot(px - cx, py - cy) - radius


def diamond_sdf(px: float, py: float, cx: float, cy: float, rx: float, ry: float) -> float:
    return (abs(px - cx) / rx + abs(py - cy) / ry - 1.0) * min(rx, ry)


def dist_to_segment(px: float, py: float, ax: float, ay: float, bx: float, by: float) -> float:
    vx = bx - ax
    vy = by - ay
    length_sq = vx * vx + vy * vy
    if length_sq == 0:
        return math.hypot(px - ax, py - ay)
    t = clamp(((px - ax) * vx + (py - ay) * vy) / length_sq)
    proj_x = ax + vx * t
    proj_y = ay + vy * t
    return math.hypot(px - proj_x, py - proj_y)


def fill_alpha(dist: float, softness: float) -> float:
    return clamp(1.0 - dist / softness)


def band_alpha(inner: float, outer: float, softness: float) -> float:
    return clamp(fill_alpha(outer, softness) - fill_alpha(inner, softness))


def line_alpha(distance: float, radius: float, softness: float = 0.008) -> float:
    return clamp(1.0 - (distance - radius) / softness)


def background_color(x: float, y: float) -> tuple[float, float, float]:
    if y < 0.42:
        base = mix_rgb(BG_TOP, BG_MID, y / 0.42)
    else:
        base = mix_rgb(BG_MID, BG_BOTTOM, (y - 0.42) / 0.58)

    top_glow_dist = math.hypot(x - 0.32, y - 0.16)
    top_glow = clamp(1.0 - top_glow_dist / 0.46) ** 2
    base = overlay(base, BG_GLOW, 0.30 * top_glow)

    center_glow_dist = math.hypot(x - 0.50, y - 0.48)
    center_glow = clamp(1.0 - center_glow_dist / 0.58) ** 2
    base = overlay(base, GRID_GLOW, 0.08 * center_glow)

    vignette_dist = math.hypot(x - 0.52, y - 0.58)
    vignette = clamp((vignette_dist - 0.16) / 0.55)
    base = overlay(base, VIGNETTE, vignette * 0.56)
    return base


def add_route(
    color: tuple[float, float, float],
    x: float,
    y: float,
    ax: float,
    ay: float,
    bx: float,
    by: float,
    base_rgb: tuple[int, int, int],
    hi_rgb: tuple[int, int, int],
    shadow_strength: float,
    active: bool,
) -> tuple[float, float, float]:
    shadow_dist = dist_to_segment(x, y, ax + 0.012, ay + 0.014, bx + 0.012, by + 0.014)
    shadow = line_alpha(shadow_dist, 0.034 if active else 0.028, softness=0.016) * shadow_strength
    color = overlay(color, ROUTE_SHADOW, shadow)

    dist = dist_to_segment(x, y, ax, ay, bx, by)
    color = overlay(color, base_rgb, line_alpha(dist, 0.028 if active else 0.022) * (0.94 if active else 0.38))
    color = overlay(color, hi_rgb, line_alpha(dist, 0.015 if active else 0.012) * (0.56 if active else 0.12))
    return color


def add_node(
    color: tuple[float, float, float],
    x: float,
    y: float,
    cx: float,
    cy: float,
    ring_rgb: tuple[int, int, int],
    glow_rgb: tuple[int, int, int],
    glow_strength: float,
) -> tuple[float, float, float]:
    glow = fill_alpha(circle_sdf(x, y, cx, cy, 0.095), 0.018) * glow_strength
    color = overlay(color, glow_rgb, glow)

    outer = circle_sdf(x, y, cx, cy, 0.056)
    inner = circle_sdf(x, y, cx, cy, 0.038)
    color = overlay(color, ring_rgb, band_alpha(inner, outer, 0.010) * 0.95)
    color = overlay(color, NODE_CORE, fill_alpha(inner, 0.010) * 0.98)

    core = circle_sdf(x, y, cx, cy, 0.018)
    color = overlay(color, ring_rgb, fill_alpha(core, 0.008) * 0.95)
    return color


def sample_icon(x: float, y: float) -> tuple[int, int, int, int]:
    shell_dist = round_rect_sdf(x, y, half_w=0.41, half_h=0.41, radius=0.16)
    if shell_dist > 0.0:
        return (0, 0, 0, 0)

    color = background_color(x, y)

    border = clamp(1.0 - (-shell_dist / 0.026))
    color = overlay(color, BORDER, 0.90 * border)

    panel = round_rect_sdf(x, y, half_w=0.33, half_h=0.33, radius=0.10)
    color = overlay(color, PANEL, fill_alpha(panel, 0.014) * 0.62)

    if 0.18 <= x <= 0.82 and 0.18 <= y <= 0.82:
        for pos in GRID_POSITIONS:
            vx = abs(x - pos)
            hy = abs(y - pos)
            color = overlay(color, GRID, clamp(1.0 - vx / 0.010) * 0.18)
            color = overlay(color, GRID_GLOW, clamp(1.0 - vx / 0.020) * 0.05)
            color = overlay(color, GRID, clamp(1.0 - hy / 0.010) * 0.18)
            color = overlay(color, GRID_GLOW, clamp(1.0 - hy / 0.020) * 0.05)

    for cx, cy in (NODE_TL, NODE_TR, NODE_BL, NODE_BR):
        glow = clamp(1.0 - math.hypot(x - cx, y - cy) / 0.08) ** 2
        color = overlay(color, GRID_GLOW, glow * 0.06)

    inactive_segments = (
        (*NODE_TR, *CENTER),
        (*CENTER, *NODE_BL),
    )
    for ax, ay, bx, by in inactive_segments:
        color = add_route(color, x, y, ax, ay, bx, by, ROUTE_IDLE, ROUTE_IDLE_HI, 0.16, active=False)

    active_segments = (
        (*NODE_TL, *CENTER),
        (*CENTER, *NODE_BR),
    )
    for ax, ay, bx, by in active_segments:
        color = add_route(color, x, y, ax, ay, bx, by, ROUTE_ACTIVE, ROUTE_ACTIVE_HI, 0.34, active=True)

    color = add_node(color, x, y, *NODE_TL, NODE_ACTIVE, ROUTE_ACTIVE_HI, 0.22)
    color = add_node(color, x, y, *NODE_BR, NODE_ACTIVE, ROUTE_ACTIVE_HI, 0.22)
    color = add_node(color, x, y, *NODE_TR, NODE_IDLE, ROUTE_IDLE_HI, 0.14)
    color = add_node(color, x, y, *NODE_BL, NODE_IDLE, ROUTE_IDLE_HI, 0.14)

    dispatch_shadow = diamond_sdf(x, y, CENTER[0] + 0.010, CENTER[1] + 0.012, 0.102, 0.102)
    color = overlay(color, ROUTE_SHADOW, fill_alpha(dispatch_shadow, 0.016) * 0.34)

    dispatch_outer = diamond_sdf(x, y, CENTER[0], CENTER[1], 0.092, 0.092)
    dispatch_inner = diamond_sdf(x, y, CENTER[0], CENTER[1], 0.066, 0.066)
    color = overlay(color, ROUTE_ACTIVE, band_alpha(dispatch_inner, dispatch_outer, 0.010) * 0.95)
    color = overlay(color, NODE_CORE, fill_alpha(dispatch_inner, 0.010) * 0.98)

    dispatch_glow = diamond_sdf(x, y, CENTER[0], CENTER[1], 0.116, 0.116)
    color = overlay(color, ROUTE_ACTIVE_HI, fill_alpha(dispatch_glow, 0.018) * 0.10)

    packet = round_rect_sdf(x - CENTER[0] + 0.5, y - CENTER[1] + 0.5, half_w=0.024, half_h=0.024, radius=0.008)
    color = overlay(color, ROUTE_ACTIVE_HI, fill_alpha(packet, 0.008) * 0.98)

    return tuple(int(round(channel)) for channel in (*color, 255))


def render_png_bytes(size: int) -> bytes:
    image = bytearray()
    samples = ((0.25, 0.25), (0.75, 0.25), (0.25, 0.75), (0.75, 0.75))

    for py in range(size):
        for px in range(size):
            accum = [0.0, 0.0, 0.0, 0.0]
            for sx, sy in samples:
                sample = sample_icon((px + sx) / size, (py + sy) / size)
                for index, value in enumerate(sample):
                    accum[index] += value
            for value in accum:
                image.append(int(round(value / len(samples))))

    raw = bytearray()
    stride = size * 4
    for row in range(size):
        raw.append(0)
        start = row * stride
        raw.extend(image[start : start + stride])

    def chunk(kind: bytes, payload: bytes) -> bytes:
        checksum = zlib.crc32(kind)
        checksum = zlib.crc32(payload, checksum)
        return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", checksum & 0xFFFFFFFF)

    ihdr = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
    return b"".join(
        (
            b"\x89PNG\r\n\x1a\n",
            chunk(b"IHDR", ihdr),
            chunk(b"IDAT", zlib.compress(bytes(raw), level=9)),
            chunk(b"IEND", b""),
        )
    )


def render_ico_bytes() -> bytes:
    icon_sizes = (16, 32, 48)
    pngs = [(size, render_png_bytes(size)) for size in icon_sizes]
    header = bytearray(struct.pack("<HHH", 0, 1, len(pngs)))
    payload = bytearray()
    offset = 6 + 16 * len(pngs)

    for size, png in pngs:
        header.extend(struct.pack("<BBBBHHII", size, size, 0, 0, 1, 32, len(png), offset))
        offset += len(png)
        payload.extend(png)

    return bytes(header + payload)


def svg_markup() -> str:
    return """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none">
  <defs>
    <linearGradient id="cg-bg" x1="32" y1="6" x2="32" y2="58" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#3f576a"/>
      <stop offset=".42" stop-color="#1c2c38"/>
      <stop offset="1" stop-color="#091015"/>
    </linearGradient>
    <radialGradient id="cg-glow" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="translate(22 14) rotate(52) scale(26 22)">
      <stop stop-color="#7a9bad" stop-opacity=".52"/>
      <stop offset="1" stop-color="#7a9bad" stop-opacity="0"/>
    </radialGradient>
    <filter id="cg-shadow" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="1.4" dy="1.6" stdDeviation="1.4" flood-color="#391f0a" flood-opacity=".55"/>
    </filter>
    <filter id="cg-soft" x="-20%" y="-20%" width="140%" height="140%">
      <feDropShadow dx="0" dy="0" stdDeviation="1.5" flood-color="#fde0a3" flood-opacity=".18"/>
    </filter>
  </defs>
  <rect x="6" y="6" width="52" height="52" rx="12" fill="url(#cg-bg)" stroke="#7c95a8" stroke-width="2"/>
  <rect x="6" y="6" width="52" height="52" rx="12" fill="url(#cg-glow)"/>
  <rect x="12" y="12" width="40" height="40" rx="7" fill="#142029" fill-opacity=".55"/>
  <g opacity=".42" stroke="#52697c" stroke-width="1.8" stroke-linecap="round">
    <path d="M18 18H46"/>
    <path d="M18 32H46"/>
    <path d="M18 46H46"/>
    <path d="M18 18V46"/>
    <path d="M32 18V46"/>
    <path d="M46 18V46"/>
  </g>
  <g stroke-linecap="round" stroke-linejoin="round" fill="none">
    <path d="M46 18L32 32L18 46" stroke="#5c798f" stroke-opacity=".52" stroke-width="3.2"/>
    <path d="M46 18L32 32L18 46" stroke="#acc3d2" stroke-opacity=".18" stroke-width="1.2"/>
    <g filter="url(#cg-shadow)">
      <path d="M18 18L32 32L46 46" stroke="#e09b2d" stroke-width="4.2"/>
      <path d="M18 18L32 32L46 46" stroke="#fde0a3" stroke-opacity=".54" stroke-width="1.7"/>
    </g>
  </g>
  <g filter="url(#cg-soft)">
    <circle cx="18" cy="18" r="4.1" fill="#131c23" stroke="#f8cc70" stroke-width="2.1"/>
    <circle cx="46" cy="46" r="4.1" fill="#131c23" stroke="#f8cc70" stroke-width="2.1"/>
  </g>
  <circle cx="46" cy="18" r="4.1" fill="#131c23" stroke="#adc5d5" stroke-width="2.1"/>
  <circle cx="18" cy="46" r="4.1" fill="#131c23" stroke="#adc5d5" stroke-width="2.1"/>
  <g filter="url(#cg-shadow)">
    <path d="M32 24L40 32L32 40L24 32L32 24Z" fill="#131c23" stroke="#e09b2d" stroke-width="2.1"/>
  </g>
  <rect x="29" y="29" width="6" height="6" rx="1.4" fill="#fde0a3"/>
</svg>
"""


def write_file(path: Path, content: bytes) -> None:
    path.write_bytes(content)


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate Clawgrid favicon assets.")
    parser.add_argument("--out-dir", type=Path, default=DEFAULT_OUT_DIR, help="Directory to write favicon assets into.")
    args = parser.parse_args()

    out_dir = args.out_dir
    out_dir.mkdir(parents=True, exist_ok=True)

    svg_path = out_dir / "favicon.svg"
    svg_path.write_text(svg_markup(), encoding="utf-8")

    png_specs = {
        "favicon-16x16.png": 16,
        "favicon-32x32.png": 32,
        "apple-touch-icon.png": 180,
        "android-chrome-192x192.png": 192,
        "android-chrome-512x512.png": 512,
    }

    for filename, size in png_specs.items():
        write_file(out_dir / filename, render_png_bytes(size))

    write_file(out_dir / "favicon.ico", render_ico_bytes())


if __name__ == "__main__":
    main()
