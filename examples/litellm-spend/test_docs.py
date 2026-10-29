# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
"""Keep the release landing page and relocated documentation linked."""

from pathlib import Path
import re
import unittest
from urllib.parse import unquote, urlsplit

from test_spend import WRITER_OBSERVATIONS


ROOT = Path(__file__).resolve().parents[2]


def anchors(text):
    headings = re.findall(r"^#{1,6} (.+)$", text, re.M)
    return {re.sub(r"[^\w\s-]", "", h.lower()).replace(" ", "-")
            for h in headings} | set(re.findall(r'<a id="([^"]+)"', text))


class DocumentationTests(unittest.TestCase):
    def test_pinned_usage_table_matches_database_expectations(self):
        readme = Path(__file__).with_name("README.md").read_text()
        expected = []
        for family, (_, modes) in WRITER_OBSERVATIONS.items():
            for label, values in zip(("Positive", "Zero", "Absent"), modes.values()):
                cells = [family, label, *("NULL" if value is None else str(value)
                                         for value in values)]
                expected.append("| " + " | ".join(cells) + " |")
        actual = [line for line in readme.splitlines()
                  if any(line.startswith("| " + family + " |")
                         for family in WRITER_OBSERVATIONS)]
        # The separate normalization table has a different column count.
        actual = [line for line in actual if line.count("|") == 7]
        self.assertEqual(actual, expected)
        for name in ("README.md", "TESTING.md"):
            content = Path(__file__).with_name(name).read_text()
            self.assertIn("usage_details=false", content)
            self.assertIn("usage_details=true", content)
            self.assertIn("v1.104.0", content)
        self.assertIn("\\set usage_details true",
                      Path(__file__).with_name("export.sql").read_text())

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
            "1-export-request-level-rows", "3-investigate",
            "2-install-v060", "2-install-v061", "2-install-v070",
            "2-install-v080", "2-install-v090", "2-install-v0100",
            "2-install-v0110", "1-install-v0120",
        } <= anchors(readme))

    def test_native_release_promotion(self):
        readme = (ROOT / "README.md").read_text()
        self.assertLess(readme.index("### 1. Install v0.14.0"),
                        readme.index("investigate --litellm-db"))
        self.assertIn("Manual Alternative: Export With psql", readme)
        self.assertNotIn("go build", readme)
        notes = (ROOT / "docs/releases/v0.12.0.md").read_text()
        self.assertIn("2026-10-18", notes)
        for phrase in ("first-seen aliases", "tied", "identity hashing"):
            self.assertIn(phrase, notes)
        self.assertIn("## [0.12.0] - 2026-10-18",
                      (ROOT / "CHANGELOG.md").read_text())
        for name in ("README.md", "SECURITY.md", "docs/NATIVE_EXPORT.md",
                     "docs/OPERATIONS.md", "docs/INSPECT.md", "docs/COMPARISON.md",
                     "docs/releases/v0.12.0.md", "examples/litellm-spend/README.md",
                     "examples/litellm-spend/TESTING.md"):
            with self.subTest(document=name):
                content = (ROOT / name).read_text()
                self.assertNotRegex(content.lower(),
                                    r"unreleased v0\.12|native candidate|candidate (?:build|recipe|checkout)")

    def test_v014_preparation_and_agent_guide(self):
        readme = (ROOT / "README.md").read_text()
        installer = (ROOT / "scripts/install.sh").read_text()
        self.assertIn("v0.14.0", installer)
        self.assertNotIn("v0.13.1", installer)
        self.assertIn("publication", readme)
        self.assertIn("v0.14.0", (ROOT / "docs/ROW_COVERAGE.md").read_text())
        guide = (ROOT / "docs/AGENT_ATTRIBUTION.md").read_text()
        self.assertLess(guide.index("300 to 620 tokens"), guide.index("## Run"))
        for phrase in ("200 of the 320-token", "62.5%", "project-1", "project-2",
                       "--show-labels --acknowledge-sensitive-labels"):
            self.assertIn(phrase, guide)
        notes = (ROOT / "docs/releases/v0.14.0.md").read_text()
        self.assertIn("## Breaking Routing Label Disclosure", notes)
        self.assertIn("--show-labels --acknowledge-sensitive-labels", notes)
        self.assertIn("Not tagged or published", notes)

    def test_langfuse_guide_release_pins(self):
        readme = (ROOT / "README.md").read_text()
        versions = re.findall(r"^### 1\. Install (v\d+\.\d+\.\d+)$", readme, re.M)
        self.assertEqual(len(versions), 1)
        version = versions[0]
        anchor = "1-install-" + version.replace(".", "")
        self.assertIn(anchor, anchors(readme))
        guide = (ROOT / "docs/LANGFUSE.md").read_text()
        self.assertEqual(re.findall(
            r"^git clone --depth 1 --branch (\S+) https://github\.com/llm-measurement/fleetdiff$",
            guide, re.M), [version])
        self.assertEqual(re.findall(
            r"\[README\]\(\.\./README\.md#([^)]+)\)", guide), [anchor])

    def test_document_links(self):
        paths = [ROOT / "README.md", ROOT / "docs/WORKFLOWS.md",
                 ROOT / "docs/OPERATIONS.md", ROOT / "docs/RELEASING.md",
                 ROOT / "docs/releases/v0.6.0.md", ROOT / "docs/releases/v0.7.0.md",
                 ROOT / "docs/releases/v0.8.0.md", ROOT / "docs/SESSION_BUDGET.md",
                 ROOT / "docs/CACHE.md", Path(__file__).with_name("README.md")]
        paths.extend([ROOT / "docs/ROW_COVERAGE.md", ROOT / "docs/releases/v0.9.0.md",
                      ROOT / "docs/WHAT_IF.md", ROOT / "docs/releases/v0.10.0.md"])
        paths.extend(ROOT / name for name in (
            "SECURITY.md", "CHANGELOG.md", "docs/NATIVE_EXPORT.md", "docs/ROUTING.md",
            "docs/INSPECT.md", "docs/COMPARISON.md", "docs/releases/v0.11.0.md",
            "docs/releases/v0.12.0.md", "docs/releases/v0.13.1.md",
            "docs/LANGFUSE.md", "docs/AGENT_ATTRIBUTION.md", "docs/releases/v0.14.0.md",
            "examples/litellm-spend/TESTING.md"))
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
