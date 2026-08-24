from __future__ import annotations

import os
from pathlib import Path

import pytest

from diffvouch.errors import CoverageError
from diffvouch.git import chunk_patch, collect_diff
from diffvouch.sanitize import redact_secrets


def test_working_tree_collects_tracked_untracked_and_symlink(
    git_repo: Path, tmp_path: Path
) -> None:
    (git_repo / "tracked.txt").write_text("changed\n", encoding="utf-8")
    (git_repo / "new.txt").write_text("new file\n", encoding="utf-8")
    outside = tmp_path.parent / "outside.txt"
    outside.write_text("must-not-be-read\n", encoding="utf-8")
    os.symlink(outside, git_repo / "link.txt")

    result = collect_diff(root=git_repo)

    assert result.mode == "working-tree"
    assert result.reviewed_files == ["link.txt", "new.txt", "tracked.txt"]
    assert "+must-not-be-read" not in result.patch
    assert f"+{outside}" in result.patch
    assert "new file mode 120000" in result.patch


def test_untracked_symlink_target_cannot_inject_a_patch_boundary(git_repo: Path) -> None:
    os.symlink("target\ndiff --git a/fake b/fake", git_repo / "link.txt")

    result = collect_diff(root=git_repo)

    assert result.reviewed_files == ["link.txt"]
    assert "+diff --git a/fake b/fake" in result.patch


def test_binary_untracked_file_is_reported_but_not_submitted(git_repo: Path) -> None:
    (git_repo / "image.bin").write_bytes(b"abc\0def")

    result = collect_diff(root=git_repo)

    assert result.patch == ""
    assert result.binary_files == ["image.bin"]


def test_modified_tracked_binary_is_reported(git_repo: Path) -> None:
    import subprocess

    binary = git_repo / "tracked.bin"
    binary.write_bytes(b"before\0data")
    subprocess.run(["git", "add", "tracked.bin"], cwd=git_repo, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "add binary"], cwd=git_repo, check=True)
    binary.write_bytes(b"after\0data")

    result = collect_diff(root=git_repo)

    assert result.patch == ""
    assert result.binary_files == ["tracked.bin"]


def test_binary_marker_text_inside_source_is_not_misclassified(git_repo: Path) -> None:
    (git_repo / "source.py").write_text('marker = "Binary files differ"\n', encoding="utf-8")

    result = collect_diff(root=git_repo)

    assert "source.py" in result.reviewed_files
    assert "source.py" not in result.binary_files


def test_non_utf8_text_is_reported_as_unreviewable(git_repo: Path) -> None:
    (git_repo / "tracked.txt").write_bytes(b"changed-\xff\n")

    result = collect_diff(root=git_repo)

    assert result.patch == ""
    assert result.binary_files == ["tracked.txt"]


def test_staged_only_excludes_unstaged_and_untracked(git_repo: Path) -> None:
    (git_repo / "tracked.txt").write_text("staged\n", encoding="utf-8")
    import subprocess

    subprocess.run(["git", "add", "tracked.txt"], cwd=git_repo, check=True)
    (git_repo / "tracked.txt").write_text("unstaged\n", encoding="utf-8")
    (git_repo / "new.txt").write_text("new\n", encoding="utf-8")

    result = collect_diff(root=git_repo, staged_only=True)

    assert "+staged" in result.patch
    assert "+unstaged" not in result.patch
    assert "new.txt" not in result.patch


def test_diff_limit_fails_closed(git_repo: Path) -> None:
    (git_repo / "tracked.txt").write_text("x" * 20_000, encoding="utf-8")

    with pytest.raises(CoverageError, match="safety limit"):
        collect_diff(root=git_repo, max_diff_bytes=10_000)


def test_chunk_patch_preserves_all_file_sections() -> None:
    section_a = "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n"
    section_b = "diff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n-old\n+new\n"

    chunks = chunk_patch(section_a + section_b, len(section_a.encode()) + 1)

    assert chunks == [section_a, section_b]


def test_secret_redaction() -> None:
    sanitized, count = redact_secrets("+OPENAI_API_KEY=sk-supersecretvalue1234567890\n")

    assert "supersecret" not in sanitized
    assert count == 1


def test_prefixed_and_fine_grained_secrets_are_redacted() -> None:
    fine_grained_pat = "github_pat_" + "A" * 30
    source = f"+DATABASE_PASSWORD=database-secret-123\n+TOKEN={fine_grained_pat}\n"

    sanitized, count = redact_secrets(source)

    assert "database-secret-123" not in sanitized
    assert fine_grained_pat not in sanitized
    assert count == 2


def test_punctuation_aws_and_encrypted_keys_are_redacted() -> None:
    aws_key = "AKIA" + "A" * 16
    source = (
        '+DATABASE_PASSWORD="p@ss! word#with$punctuation"\n'
        f"+AWS_ACCESS_KEY_ID={aws_key}\n"
        "+-----BEGIN ENCRYPTED PRIVATE KEY-----\n"
        "+ciphertext\n"
        "+-----END ENCRYPTED PRIVATE KEY-----\n"
    )

    sanitized, count = redact_secrets(source)

    assert "p@ss!" not in sanitized
    assert aws_key not in sanitized
    assert "ciphertext" not in sanitized
    assert sanitized.count("\n") == source.count("\n")
    assert count == 3


def test_quoted_secret_redaction_preserves_quotes() -> None:
    sanitized, count = redact_secrets('+API_KEY="supersecretvalue123"\n')

    assert "supersecret" not in sanitized
    assert 'API_KEY="[REDACTED BY DIFFVOUCH]"' in sanitized
    assert count == 1


def test_secret_reference_source_code_is_not_rewritten() -> None:
    source = 'store_secret(f"api-key:{args.provider}", secret)'

    sanitized, count = redact_secrets(source)

    assert sanitized == source
    assert count == 0


def test_private_key_diff_is_redacted() -> None:
    patch = "+-----BEGIN PRIVATE KEY-----\n+sensitive-material\n+-----END PRIVATE KEY-----\n"

    sanitized, count = redact_secrets(patch)

    assert "sensitive-material" not in sanitized
    assert count == 1
    assert sanitized.count("\n") == patch.count("\n")
    assert all(line.startswith("+") for line in sanitized.splitlines())
