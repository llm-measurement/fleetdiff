# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

"""Render a timed playback of actual demo output, not a performance recording."""

import argparse
import io
from pathlib import Path
import re
import shutil
import subprocess

from PIL import Image, ImageDraw, ImageFont


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("transcript", type=Path)
    parser.add_argument("--ffmpeg", default=shutil.which("ffmpeg"))
    parser.add_argument("--font", type=Path)
    parser.add_argument("--out", type=Path, default=Path(__file__).resolve().parent)
    args = parser.parse_args()
    if not args.ffmpeg:
        parser.error("install FFmpeg or supply --ffmpeg")
    fonts = [args.font] if args.font else [
        Path("/System/Library/Fonts/Menlo.ttc"),
        Path("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"),
    ]
    font_path = next((p for p in fonts if p.is_file()), None)
    if font_path is None:
        parser.error("supply a monospace font with --font")
    font = ImageFont.truetype(str(font_path), 24)
    small = ImageFont.truetype(str(font_path), 19)
    transcript = args.transcript.read_text(encoding="utf-8")
    blocks = re.split(r"\n(?=[1-5]\. )", transcript.rstrip())
    if len(blocks) != 6 or any(
        not block.startswith(f"{index}. ") for index, block in enumerate(blocks[1:], 1)
    ):
        parser.error("expected the five-question output from sh examples/demo.sh")
    stages = [blocks[0].rstrip() + "\n\n" + block.strip() for block in blocks[1:]]

    frames = []
    for index, stage in enumerate(stages):
        frame = Image.new("RGB", (1280, 720), "#171b20")
        draw = ImageDraw.Draw(frame)
        draw.text((40, 28), "$ sh examples/demo.sh", font=font, fill="#92e1b5")
        draw.line((40, 72, 1240, 72), fill="#46515c", width=1)
        lines = stage.splitlines()
        if len(lines) > 19 or any(draw.textlength(line, font=font) > 1200 for line in lines):
            parser.error("transcript no longer fits; adjust the rendering layout")
        for line_number, line in enumerate(lines):
            color = "#92e1b5" if line.startswith(("1.", "2.", "3.", "4.", "5.")) else "#f2f4f6"
            draw.text((40, 100 + line_number * 28), line, font=font, fill=color)
        draw.text((40, 674), "Sample data | Timed playback, not a speed test", font=small, fill="#b4bdc6")
        draw.text((1160, 674), f"{index + 1} / {len(stages)}", font=small, fill="#b4bdc6")
        frames.append(frame)

    args.out.mkdir(parents=True, exist_ok=True)
    frames[0].save(args.out / "preview.png")
    (args.out / "transcript.txt").write_text(transcript, encoding="utf-8")
    command = [
        args.ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
        "-f", "image2pipe", "-vcodec", "mjpeg", "-r", "1", "-i", "pipe:0",
        "-an", "-c:v", "libvpx", "-b:v", "1500k", "-pix_fmt", "yuv420p",
        "-threads", "2", str(args.out / "walkthrough.webm"),
    ]
    with subprocess.Popen(command, stdin=subprocess.PIPE) as encoder:
        try:
            for frame in frames:
                jpeg = io.BytesIO()
                frame.save(jpeg, format="JPEG", quality=95)
                for _ in range(12):
                    encoder.stdin.write(jpeg.getvalue())
        finally:
            encoder.stdin.close()
        if encoder.wait() != 0:
            raise SystemExit("FFmpeg failed")


if __name__ == "__main__":
    main()
