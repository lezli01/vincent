#!/usr/bin/env python3
"""Generate deterministic social cards for the "Why vincent is awesome" series."""

from __future__ import annotations

import json
import os
from pathlib import Path

try:
    from PIL import Image, ImageDraw, ImageEnhance, ImageFilter, ImageFont
except ImportError as exc:  # pragma: no cover - actionable local setup failure
    raise SystemExit("Pillow is required: python -m pip install Pillow") from exc


ROOT = Path(__file__).resolve().parents[1]
ARTICLES_PATH = ROOT / "_data" / "why_articles.json"
BACKGROUND_PATH = ROOT / "docs" / "assets" / "opengraph.png"
CANVAS = (1200, 630)

INK = (248, 248, 242, 255)
MUTED = (194, 190, 183, 255)
ACCENT = (139, 233, 253, 255)
PANEL = (30, 29, 28, 238)
PANEL_SOFT = (40, 38, 37, 218)


def font_candidates(bold: bool) -> list[Path]:
    override = os.environ.get("VINCENT_SOCIAL_FONT_BOLD" if bold else "VINCENT_SOCIAL_FONT")
    names = [override] if override else []
    if bold:
        names.extend(
            [
                "/System/Library/Fonts/Supplemental/Verdana Bold.ttf",
                "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
                "C:/Windows/Fonts/verdanab.ttf",
            ]
        )
    else:
        names.extend(
            [
                "/System/Library/Fonts/Supplemental/Verdana.ttf",
                "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
                "C:/Windows/Fonts/verdana.ttf",
            ]
        )
    return [Path(name) for name in names if name]


def load_font(size: int, *, bold: bool = False) -> ImageFont.FreeTypeFont:
    for candidate in font_candidates(bold):
        if candidate.exists():
            return ImageFont.truetype(str(candidate), size=size)
    raise SystemExit(
        "No suitable font found. Set VINCENT_SOCIAL_FONT and "
        "VINCENT_SOCIAL_FONT_BOLD to TrueType font paths."
    )


def wrap_text(draw: ImageDraw.ImageDraw, text: str, font: ImageFont.FreeTypeFont, width: int) -> list[str]:
    lines: list[str] = []
    current = ""
    for word in text.split():
        candidate = f"{current} {word}".strip()
        if current and draw.textlength(candidate, font=font) > width:
            lines.append(current)
            current = word
        else:
            current = candidate
    if current:
        lines.append(current)
    return lines


def fit_title(draw: ImageDraw.ImageDraw, title: str) -> tuple[ImageFont.FreeTypeFont, list[str], int]:
    for size in range(58, 37, -2):
        font = load_font(size, bold=True)
        lines = wrap_text(draw, title, font, 710)
        bbox = draw.textbbox((0, 0), "Ag", font=font)
        line_height = bbox[3] - bbox[1] + 13
        if len(lines) <= 4 and len(lines) * line_height <= 290:
            return font, lines, line_height
    font = load_font(36, bold=True)
    lines = wrap_text(draw, title, font, 710)
    return font, lines, 49


def render_card(article: dict[str, object], total: int) -> Path:
    background = Image.open(BACKGROUND_PATH).convert("RGB").resize(CANVAS, Image.Resampling.LANCZOS)
    background = ImageEnhance.Brightness(background).enhance(0.64).filter(ImageFilter.GaussianBlur(1.2))
    card = background.convert("RGBA")
    overlay = Image.new("RGBA", CANVAS, (0, 0, 0, 48))
    card = Image.alpha_composite(card, overlay)
    draw = ImageDraw.Draw(card)

    draw.rectangle((322, 42, 1158, 588), fill=PANEL, outline=ACCENT, width=3)
    draw.rectangle((340, 60, 1140, 570), outline=(97, 92, 87, 255), width=1)
    draw.rectangle((54, 486, 293, 570), fill=PANEL_SOFT, outline=(97, 92, 87, 255), width=1)

    label_font = load_font(22, bold=True)
    small_font = load_font(18)
    count_font = load_font(20, bold=True)
    draw.text((374, 91), "// why vincent is awesome", font=label_font, fill=ACCENT)

    title_font, title_lines, line_height = fit_title(draw, str(article["title"]))
    title_y = 159
    for line in title_lines:
        draw.text((374, title_y), line, font=title_font, fill=INK)
        title_y += line_height

    draw.line((374, 492, 1106, 492), fill=(97, 92, 87, 255), width=2)
    draw.text((374, 517), "vincent · agentic workload orchestration", font=small_font, fill=MUTED)
    count = f"{int(article['order']):02d} / {total:02d}"
    count_width = draw.textlength(count, font=count_font)
    draw.text((1106 - count_width, 516), count, font=count_font, fill=ACCENT)

    draw.text((75, 506), "DURABLE", font=small_font, fill=MUTED)
    draw.text((75, 535), "WORKFLOWS", font=count_font, fill=ACCENT)

    output = ROOT / str(article["image"]).lstrip("/")
    output.parent.mkdir(parents=True, exist_ok=True)
    card.convert("RGB").save(output, "PNG", optimize=True)
    return output


def main() -> None:
    articles = json.loads(ARTICLES_PATH.read_text(encoding="utf-8"))
    if not isinstance(articles, list) or not articles:
        raise SystemExit(f"No article metadata found in {ARTICLES_PATH}")

    outputs = [render_card(article, len(articles)) for article in articles]
    print(f"Generated {len(outputs)} social cards:")
    for output in outputs:
        print(output.relative_to(ROOT))


if __name__ == "__main__":
    main()
