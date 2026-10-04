# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

"""Make the README GIF from actual sessions-demo output, with readable pauses."""

import argparse
from pathlib import Path
import subprocess
import textwrap

from PIL import Image, ImageDraw, ImageFont


ROOT = Path(__file__).resolve().parents[2]
COMMAND = "sh examples/investigate.sh --sessions"
SIZE = (1280, 650)
INK, MUTED, GREEN = "#e6edf3", "#a4adb8", "#86dfad"


def sections(transcript):
    blocks = transcript.strip().split("\n\n")
    if not blocks[0].startswith("Synthetic demo:") or len(blocks) < 3:
        raise ValueError("expected output from the synthetic sessions demo")
    overview = blocks[1].splitlines()
    sessions = next((b.splitlines() for b in blocks if b.startswith("Which sessions need investigation?")), [])
    if not overview or "tracked sessions flagged for review:" not in overview[0]:
        raise ValueError("session headline is missing")
    if len(sessions) < 3 or not any("flagged for review" in row for row in sessions[2:]):
        raise ValueError("expected a session table with a flagged candidate")
    return overview, sessions


def load_font(path=None):
    candidates = [Path(path)] if path else [
        Path("/System/Library/Fonts/Menlo.ttc"),
        Path("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"),
    ]
    font_path = next((p for p in candidates if p.is_file()), None)
    if font_path is None:
        raise ValueError("supply a monospace font with --font")
    return ImageFont.truetype(str(font_path), 23)


def frame(lines, font):
    image = Image.new("RGB", SIZE, "#171b20")
    draw = ImageDraw.Draw(image)

    def line(y, text, color=INK):
        bounds = draw.textbbox((32, y), text, font=font)
        if bounds[2] > SIZE[0] - 32 or bounds[3] > SIZE[1] - 20:
            raise ValueError("demo output outgrew the GIF layout")
        draw.text((32, y), text, font=font, fill=color)

    line(25, "$ " + COMMAND, GREEN)
    draw.line((32, 70, SIZE[0] - 32, 70), fill="#46515c")
    y = 98
    for text in lines:
        if y > 548:
            raise ValueError("too many lines for the GIF layout")
        color = GREEN if "flagged for review" in text or text.startswith("Which sessions") else INK
        if text.lstrip().startswith("session-") and "flagged for review" in text:
            draw.rectangle((24, y - 3, SIZE[0] - 24, y + 29), fill="#19382b")
        line(y, text, color)
        y += 32
    draw.line((32, 592, SIZE[0] - 32, 592), fill="#46515c")
    line(600, "Synthetic demo | Actual output, timed playback", MUTED)
    return image


def render(transcript, font):
    overview, sessions = sections(transcript)
    # Wrap prose only; preserve the actual tabwriter table's column alignment.
    intro = []
    for text in overview:
        intro.extend(textwrap.wrap(text, width=86, subsequent_indent="  ",
                                   break_long_words=False, break_on_hyphens=False) or [""])
    frames = [frame(intro, font)]
    durations = [4000]
    heading = [overview[0], overview[1], "", *sessions[:2]]
    rows = sessions[2:]
    stops = sorted({1, min(3, len(rows)), min(5, len(rows)), len(rows)})
    for count in stops:
        frames.append(frame([*heading, *rows[:count]], font))
        durations.append(8000 if count == len(rows) else 350)
    return frames, durations


def write_gif(transcript, font, output):
    transcript = "\n".join(line.rstrip() for line in transcript.splitlines()) + "\n"
    frames, durations = render(transcript, font)
    # A shared palette keeps colors stable between frames and the download small.
    palette = frames[-1].quantize(colors=64)
    indexed = [f.quantize(palette=palette, dither=Image.Dither.NONE) for f in frames]
    output.mkdir(parents=True, exist_ok=True)
    indexed[0].save(output / "sessions.gif", save_all=True,
                    append_images=indexed[1:], duration=durations, loop=0,
                    optimize=True, disposal=1)
    (output / "sessions-transcript.txt").write_text(transcript, encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--font", type=Path)
    parser.add_argument("--out", type=Path, default=Path(__file__).resolve().parent)
    args = parser.parse_args()
    result = subprocess.run(["sh", "examples/investigate.sh", "--sessions"],
                            cwd=ROOT, capture_output=True, text=True, check=True)
    try:
        write_gif(result.stdout, load_font(args.font), args.out)
    except ValueError as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
