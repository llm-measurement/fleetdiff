# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Keep the release landing page and relocated documentation linked."""

from pathlib import Path
import re
import unittest
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[2]


def anchors(text):
    headings = re.findall(r"^#{1,6} (.+)$", text, re.M)
    return {re.sub(r"[^\w\s-]", "", h.lower()).replace(" ", "-")
            for h in headings} | set(re.findall(r'<a id="([^"]+)"', text))


class DocumentationTests(unittest.TestCase):
    def test_readme_shape_and_previous_anchors(self):
        readme = (ROOT / "README.md").read_text()
        self.assertLessEqual(len(readme.splitlines()), 200)
        self.assertGreaterEqual(len(readme.splitlines()), 150)
        self.assertEqual(readme.count("## Limits\n"), 1)
        self.assertEqual(readme.count("```text\n"), 1)
        self.assertNotIn("requires a source build", readme)
        self.assertIn("./fleetdiff-install/fleetdiff investigate", readme)
        self.assertIn("| Status | Pre-1.0;", readme)
        self.assertTrue({
            "fleetdiff", "using-litellm", "try-it-in-a-minute",
            "did-one-session-account-for-most-of-the-increase", "install-a-binary",
            "will-it-work-with-my-traces", "set-up-and-keep-watching",
            "keep-watching-litellm", "extend-to-separately-operated-agents",
            "questions-it-answers", "user-and-session-contributors",
            "how-it-works", "run-it-through-real-collectors", "use-your-own-data",
            "reading-the-results", "built-to-be-checked", "docs",
        } <= anchors(readme))

    def test_document_links(self):
        paths = [ROOT / "README.md", ROOT / "docs/WORKFLOWS.md",
                 ROOT / "docs/OPERATIONS.md", ROOT / "docs/RELEASING.md",
                 ROOT / "docs/releases/v0.6.0.md", Path(__file__).with_name("README.md")]
        for path in paths:
            for link in re.findall(r"\]\(([^)\s]+)\)", path.read_text()):
                parsed = urlsplit(link)
                if parsed.scheme:
                    continue
                target = (path.parent / unquote(parsed.path)).resolve() if parsed.path else path
                with self.subTest(document=path.name, link=link):
                    self.assertTrue(target.exists())
                    if parsed.fragment and target.suffix == ".md":
                        self.assertIn(unquote(parsed.fragment), anchors(target.read_text()))


if __name__ == "__main__":
    unittest.main()
