# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

"""Plot the synthetic two-operator demo's JSON reports, not customer exports."""

import argparse
import json
import math
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


def reported_tokens(report):
    counters = [c for c in report["counters"] if c["name"] in ("input_tokens", "output_tokens")]
    if len(counters) != 2 or len({c["name"] for c in counters}) != 2:
        raise ValueError("expected input and output token counters")
    if any(c["unit"] != "reported-tokens" for c in counters):
        raise ValueError("expected reported-token units")
    if any(type(c[side]) is not int or c[side] < 0 for c in counters for side in ("before", "after")):
        raise ValueError("expected nonnegative integer token counts")
    # Cache and reasoning tokens are subsets, not additions to these totals.
    return [sum(c[side] for c in counters) for side in ("before", "after")]


def load_series(directory):
    fleet, owned = [
        json.loads((directory / name).read_text(encoding="utf-8"))
        for name in ("comparison.json", "owned-only.json")
    ]
    if any(r["version"] != 1 or r["complete_observation_intervals"] is not True for r in (fleet, owned)):
        raise ValueError("expected complete v1 demo reports")
    for side in ("before", "after"):
        for field in ("start_unix_nano", "duration_nano"):
            if fleet[side][field] != owned[side][field]:
                raise ValueError("demo report windows differ")
    duration = fleet["before"]["duration_nano"]
    if duration <= 0 or duration != fleet["after"]["duration_nano"]:
        raise ValueError("expected equal positive window durations")
    if fleet["before"]["start_unix_nano"] + duration > fleet["after"]["start_unix_nano"]:
        raise ValueError("expected nonoverlapping windows")
    totals, ours = reported_tokens(fleet), reported_tokens(owned)
    partner = [total - own for total, own in zip(totals, ours)]
    if any(value < 0 for value in partner):
        raise ValueError("owned counts exceed fleet counts")
    if totals[0] == 0 or ours[0] == 0:
        raise ValueError("percentage annotations need nonzero before counts")
    coverage = [fleet[side]["token_coverage"] for side in ("before", "after")]
    return {"fleet": totals, "ours": ours, "partner": partner, "coverage": coverage}


def render(series):
    image = Image.new("RGB", (1200, 740), "white")
    draw = ImageDraw.Draw(image)
    fonts = {size: ImageFont.load_default(size=size) for size in (22, 25, 28, 38)}
    ink, muted, ours_color, partner_color = "#1c2930", "#53616a", "#00777e", "#a33b69"

    def label(x, y, value, size=25, color=ink, anchor="lt"):
        bounds = draw.textbbox((x, y), value, font=fonts[size], anchor=anchor)
        if bounds[0] < 0 or bounds[1] < 0 or bounds[2] > image.width or bounds[3] > image.height:
            raise ValueError("chart label exceeds image bounds")
        draw.text((x, y), value, font=fonts[size], fill=color, anchor=anchor)

    our_change = (series["ours"][1] - series["ours"][0]) / series["ours"][0]
    fleet_change = (series["fleet"][1] - series["fleet"][0]) / series["fleet"][0]
    label(48, 36, f"Your team: {our_change:+.0%}. The fleet: {fleet_change:+.0%}.", 38)
    label(48, 94, "Reported tokens across two equal time windows", color=muted)
    for x, color, name in ((48, ours_color, "Your team"), (268, partner_color, "Partner")):
        draw.rectangle((x, 149, x + 20, 169), fill=color)
        label(x + 31, 148, name, 22)

    top, bottom = 242, 566
    maximum = max(series["fleet"])
    magnitude = 10 ** math.floor(math.log10(maximum))
    step = max(1, math.ceil(maximum / 4 / magnitude * 10) * magnitude / 10)
    ceiling = step * 4
    scale = (bottom - top) / ceiling
    for tick in range(5):
        value = step * tick
        y = bottom - value * scale
        draw.line((120, y, 1148, y), fill="#dce2e5" if tick else ink, width=1)
        label(104, y, f"{value:,.0f}", 22, muted, "rm")

    for index, center in enumerate((410, 888)):
        accumulated = 0
        for key, color in (("ours", ours_color), ("partner", partner_color)):
            value = series[key][index]
            low, high = bottom - accumulated * scale, bottom - (accumulated + value) * scale
            if high < low:
                draw.rectangle((center - 116, high, center + 116, low), fill=color)
                if low - high < 36:
                    raise ValueError("demo segment too small for a readable label")
                label(center, (low + high) / 2, f"{value:,}", 28, "white", "mm")
            accumulated += value
        label(center, bottom - accumulated * scale - 16, f"{accumulated:,} total", 28, anchor="mb")
        label(center, 583, ("Before", "After")[index], 25, anchor="mt")

    before, after = series["coverage"]
    label(48, 643, "Synthetic demo. Reported tokens are not total consumption or answer quality.", 22, muted)
    label(48, 682,
          f"Missing token usage: {before['missing_usage_requests']} of {before['requests']} requests before; "
          f"{after['missing_usage_requests']} of {after['requests']} after.", 22, muted)
    return image


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reports", type=Path, help="reports directory printed by examples/demo.sh")
    parser.add_argument("--out", type=Path, default=Path(__file__).with_name("fleet-usage.png"))
    args = parser.parse_args()
    try:
        render(load_series(args.reports)).save(args.out)
    except (KeyError, ValueError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
