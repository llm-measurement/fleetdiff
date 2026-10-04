# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex

from pathlib import Path
import tempfile
import unittest

from PIL import Image

from sessions_gif import SIZE, load_font, render, sections, write_gif


TRANSCRIPT = Path(__file__).with_name("sessions-transcript.txt")


class SessionsGifTest(unittest.TestCase):
    def test_actual_headline_and_flagged_row(self):
        overview, sessions = sections(TRANSCRIPT.read_text())
        self.assertEqual(overview[0], "1 of 8 tracked sessions flagged for review: 90.91% of attributed tokens.")
        self.assertEqual(len(sessions[2:]), 8)
        self.assertEqual(sum("flagged for review" in row for row in sessions[2:]), 1)
        self.assertIn("3000", sessions[2])
        self.assertIn("90.91%", sessions[2])

    def test_animation_and_transcript(self):
        transcript = TRANSCRIPT.read_text()
        frames, durations = render(transcript, load_font())
        self.assertEqual(len(frames), 5)
        self.assertEqual(sum(durations), 13050)
        self.assertEqual(frames[0].size, SIZE)
        self.assertNotEqual(frames[0].tobytes(), frames[-1].tobytes())
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            write_gif(transcript, load_font(), output)
            self.assertEqual((output / "sessions-transcript.txt").read_text(), transcript)
            with Image.open(output / "sessions.gif") as image:
                self.assertEqual(image.n_frames, 5)
                self.assertEqual(image.size, SIZE)
                self.assertEqual(image.info["loop"], 0)
                self.assertLess((output / "sessions.gif").stat().st_size, 1024 * 1024)

    def test_unexpected_output_does_not_get_illustrated(self):
        with self.assertRaises(ValueError):
            sections("something failed")
        with self.assertRaisesRegex(ValueError, "outgrew"):
            render(TRANSCRIPT.read_text().replace("session-1", "x" * 200), load_font())


if __name__ == "__main__":
    unittest.main()
