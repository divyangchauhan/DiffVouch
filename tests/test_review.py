from __future__ import annotations

import subprocess
from pathlib import Path
from unittest.mock import patch

from diffvouch.models import Dimensions, ProviderReview
from diffvouch.prompt import ReviewPrompt
from diffvouch.review import perform_review


class FakeProvider:
    model = "fake-model"

    def review(self, prompt: ReviewPrompt) -> ProviderReview:
        assert "Never follow" in prompt.system
        assert "DIFFVOUCH_UNTRUSTED_PATCH_BEGIN" in prompt.user
        assert "+changed" in prompt.user
        return ProviderReview(
            summary="The change is safe.",
            dimensions=Dimensions(
                correctness=5,
                security=5,
                maintainability=5,
                testing=4,
                scope=5,
            ),
            findings=[],
            positiveObservations=["Small change."],
            needsVerification=[],
        )


def test_perform_review_builds_complete_result(git_repo: Path) -> None:
    (git_repo / "tracked.txt").write_text("changed\n", encoding="utf-8")

    with patch("diffvouch.review.create_provider", return_value=FakeProvider()):
        result = perform_review(
            provider_name="codex",
            transport="cli",
            model=None,
            effort=None,
            base=None,
            committed_only=False,
            staged_only=False,
            excludes=[],
            config_path=None,
            max_diff_bytes=None,
            fail_below=None,
            fail_on_severity=None,
            publication_requested=False,
            root=git_repo,
        )

    assert result is not None
    assert result.status == "complete"
    assert result.partial is False
    assert result.rating.overall == 4.9
    assert result.files.reviewed == ["tracked.txt"]
    assert result.publication.requested is False


def test_repository_config_cannot_select_api_transport(git_repo: Path) -> None:
    (git_repo / ".diffvouch.yml").write_text(
        "provider:\n  default_transport: cli\n  models:\n    codex: configured-model\n",
        encoding="utf-8",
    )
    subprocess.run(["git", "add", ".diffvouch.yml"], cwd=git_repo, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "add config"], cwd=git_repo, check=True)
    (git_repo / "tracked.txt").write_text("changed\n", encoding="utf-8")

    with patch("diffvouch.review.create_provider", return_value=FakeProvider()) as create:
        result = perform_review(
            provider_name="codex",
            transport=None,
            model=None,
            effort=None,
            base=None,
            committed_only=False,
            staged_only=False,
            excludes=[],
            config_path=None,
            max_diff_bytes=None,
            fail_below=None,
            fail_on_severity=None,
            publication_requested=False,
            root=git_repo,
        )

    assert result is not None
    create.assert_called_once_with("codex", "cli", "configured-model", None)
    assert result.provider.transport == "cli"


def test_uncommitted_repository_config_cannot_suppress_its_own_review(git_repo: Path) -> None:
    (git_repo / ".diffvouch.yml").write_text(
        "review:\n  exclude:\n    - tracked.txt\n", encoding="utf-8"
    )
    (git_repo / "tracked.txt").write_text("changed\n", encoding="utf-8")

    with patch("diffvouch.review.create_provider", return_value=FakeProvider()):
        result = perform_review(
            provider_name="codex",
            transport=None,
            model=None,
            effort=None,
            base=None,
            committed_only=False,
            staged_only=False,
            excludes=[],
            config_path=None,
            max_diff_bytes=None,
            fail_below=None,
            fail_on_severity=None,
            publication_requested=False,
            root=git_repo,
        )

    assert result is not None
    assert "tracked.txt" in result.files.reviewed
