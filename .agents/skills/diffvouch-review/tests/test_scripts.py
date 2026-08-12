#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SKILL_DIR = Path(__file__).resolve().parents[1]
COLLECTOR = SKILL_DIR / "scripts" / "collect-diff.sh"
PUBLISHER = SKILL_DIR / "scripts" / "publish-review.py"


def load_publisher():
    spec = importlib.util.spec_from_file_location("diffvouch_publish_review", PUBLISHER)
    if spec is None or spec.loader is None:
        raise RuntimeError("could not load publisher module")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class CollectorTests(unittest.TestCase):
    def make_repo(self, directory: Path) -> None:
        subprocess.run(["git", "init", "-q", "--initial-branch=main"], cwd=directory, check=True)
        subprocess.run(["git", "config", "user.name", "DiffVouch Test"], cwd=directory, check=True)
        subprocess.run(["git", "config", "user.email", "test@example.invalid"], cwd=directory, check=True)
        (directory / "tracked.txt").write_text("initial\n", encoding="utf-8")
        subprocess.run(["git", "add", "tracked.txt"], cwd=directory, check=True)
        subprocess.run(["git", "commit", "-q", "-m", "initial"], cwd=directory, check=True)

    def run_collector(self, directory: Path, limit: int) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment["DIFFVOUCH_MAX_DIFF_BYTES"] = str(limit)
        return subprocess.run(
            [str(COLLECTOR), "working-tree"],
            cwd=directory,
            env=environment,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_oversized_patch_fails_without_emitting_review_data(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            repository = Path(temporary_directory)
            self.make_repo(repository)
            (repository / "tracked.txt").write_text("x" * 2000 + "\n", encoding="utf-8")

            result = self.run_collector(repository, 100)

            self.assertEqual(result.returncode, 6)
            self.assertEqual(result.stdout, "")
            self.assertIn("review coverage cannot be guaranteed", result.stderr)

    def test_complete_patch_has_matching_size_and_end_sentinel(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            repository = Path(temporary_directory)
            self.make_repo(repository)
            (repository / "tracked.txt").write_text("changed\n", encoding="utf-8")

            result = self.run_collector(repository, 10000)

            self.assertEqual(result.returncode, 0, result.stderr)
            header_size = next(
                line.removeprefix("patch_bytes=")
                for line in result.stdout.splitlines()
                if line.startswith("patch_bytes=")
            )
            self.assertIn("partial=false", result.stdout)
            self.assertTrue(
                result.stdout.endswith(f"DIFFVOUCH_REVIEW_CONTEXT_END_V1 patch_bytes={header_size}\n")
            )


class DiffParserTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.publisher = load_publisher()

    def test_deleted_file_uses_old_path_for_left_lines(self) -> None:
        diff = """diff --git a/deleted.txt b/deleted.txt
deleted file mode 100644
index 422c2b7..0000000
--- a/deleted.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-first
-second
"""
        self.assertEqual(
            self.publisher.parse_changed_lines(diff),
            {("deleted.txt", "LEFT", 1), ("deleted.txt", "LEFT", 2)},
        )

    def test_deleted_file_decodes_git_quoted_path(self) -> None:
        diff = """diff --git \"a/caf\\303\\251 file.txt\" \"b/caf\\303\\251 file.txt\"
deleted file mode 100644
--- \"a/caf\\303\\251 file.txt\"
+++ /dev/null
@@ -3 +0,0 @@
-secret
"""
        self.assertEqual(
            self.publisher.parse_changed_lines(diff),
            {("café file.txt", "LEFT", 3)},
        )

    def test_hunk_content_that_looks_like_headers_keeps_file_path(self) -> None:
        diff = """diff --git a/counter.txt b/counter.txt
index 422c2b7..e69de29 100644
--- a/counter.txt
+++ b/counter.txt
@@ -1 +1,3 @@
 existing
+++ counter
+after
"""
        self.assertEqual(
            self.publisher.parse_changed_lines(diff),
            {("counter.txt", "RIGHT", 2), ("counter.txt", "RIGHT", 3)},
        )


if __name__ == "__main__":
    unittest.main()
