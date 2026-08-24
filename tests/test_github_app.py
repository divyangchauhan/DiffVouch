from __future__ import annotations

import subprocess
import time
from pathlib import Path
from unittest.mock import patch

import jwt
import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa

from diffvouch.credentials import SecretReference
from diffvouch.errors import GitHubError
from diffvouch.github_app import (
    GitHubAPI,
    GitHubAppConfig,
    changed_lines,
    discover_pull_request,
    parse_remote,
    publish_review,
)
from diffvouch.models import (
    Category,
    Confidence,
    Dimensions,
    FilesSummary,
    Finding,
    Gate,
    ProviderInfo,
    Publication,
    Rating,
    ReviewResult,
    Scope,
    Severity,
)


def key_pair() -> tuple[str, bytes]:
    private = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    private_pem = private.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.PKCS8,
        serialization.NoEncryption(),
    ).decode()
    public_pem = private.public_key().public_bytes(
        serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo
    )
    return private_pem, public_pem


def app_config() -> GitHubAppConfig:
    return GitHubAppConfig(
        app_id="12345",
        slug="my-diffvouch",
        host="github.com",
        api_base_url="https://api.github.com",
        web_base_url="https://github.com",
        api_version="2026-03-10",
        private_key=SecretReference(name="key", backend="file"),
    )


def test_jwt_uses_rs256_short_lived_claims() -> None:
    private_pem, public_pem = key_pair()
    token = GitHubAPI(app_config(), private_key=private_pem).jwt()

    header = jwt.get_unverified_header(token)
    payload = jwt.decode(token, public_pem, algorithms=["RS256"], audience=None)

    assert header["alg"] == "RS256"
    assert payload["iss"] == "12345"
    assert payload["iat"] <= int(time.time())
    assert payload["exp"] - payload["iat"] <= 600


def test_installation_token_is_restricted_to_repo_and_pull_requests() -> None:
    private_pem, _ = key_pair()
    api = GitHubAPI(app_config(), private_key=private_pem)
    with (
        patch.object(api, "installation_id", return_value=9),
        patch.object(api, "request", return_value={"token": "temporary"}) as request,
    ):
        assert api.installation_token("owner", "repo") == "temporary"

    assert request.call_args.kwargs["body"] == {
        "repositories": ["repo"],
        "permissions": {"pull_requests": "write"},
    }


def test_remote_parser_uses_diffvouch_name_and_strips_git(git_repo: Path) -> None:
    subprocess.run(
        ["git", "remote", "add", "origin", "git@github.com:owner/DiffVouch.git"],
        cwd=git_repo,
        check=True,
    )

    assert parse_remote(git_repo) == ("github.com", "owner", "DiffVouch")


def test_explicit_repository_does_not_require_origin(git_repo: Path) -> None:
    assert parse_remote(git_repo, "owner/repo") == ("github.com", "owner", "repo")


def test_explicit_repository_supports_enterprise_host(git_repo: Path) -> None:
    assert parse_remote(git_repo, "owner/repo", "github.example.com") == (
        "github.example.com",
        "owner",
        "repo",
    )


def test_deleted_lines_are_inline_eligible() -> None:
    diff = """diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -2 +0,0 @@
-removed
"""
    assert ("gone.txt", "LEFT", 2) in changed_lines(diff)


def test_pull_discovery_matches_fork_by_branch_and_head_sha(git_repo: Path) -> None:
    head = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=git_repo, text=True, capture_output=True, check=True
    ).stdout.strip()

    class PullAPI:
        def request(self, method: str, path: str, **kwargs):
            assert "head=" not in path
            return [
                {
                    "number": 8,
                    "head": {"ref": "main", "sha": head, "repo": {"owner": {"login": "forker"}}},
                }
            ]

    pull = discover_pull_request(PullAPI(), "token", git_repo, "upstream", "repo", None)

    assert pull["number"] == 8


def review_result() -> ReviewResult:
    finding = Finding(
        id="DV-001",
        severity=Severity.HIGH,
        category=Category.CORRECTNESS,
        blocking=True,
        title="Wrong return value",
        explanation="The new value breaks callers.",
        recommendation="Return the expected value.",
        path="app.py",
        line=2,
        side="new",
        confidence=Confidence.HIGH,
        evidence="The added line returns false.",
    )
    dimensions = Dimensions(correctness=2, security=5, maintainability=4, testing=3, scope=5)
    return ReviewResult(
        scope=Scope(
            mode="committed",
            baseRef="main",
            baseSha="a" * 40,
            mergeBase="a" * 40,
            headSha="b" * 40,
        ),
        provider=ProviderInfo(name="codex", transport="cli", model="default", effort=None),
        rating=Rating(overall=3.4, label="Needs work", dimensions=dimensions),
        summary="One blocking issue.",
        findings=[finding],
        positiveObservations=[],
        needsVerification=[],
        files=FilesSummary(reviewed=["app.py"], excluded=[], binary=[], omitted=[]),
        gate=Gate(passed=True, reasons=[]),
        publication=Publication(requested=True),
    )


class FakeAPI:
    body = None

    def __init__(self, config: GitHubAppConfig) -> None:
        self.config = config

    def installation_token(self, owner: str, repository: str) -> str:
        return "memory-only-token"

    def request(self, method: str, path: str, **kwargs):
        if method == "GET" and kwargs.get("raw"):
            return "diff --git a/app.py b/app.py\n--- a/app.py\n+++ b/app.py\n@@ -1 +1,2 @@\n old\n+new\n"
        if method == "GET":
            return {
                "number": 7,
                "state": "open",
                "head": {"sha": "b" * 40},
                "base": {"sha": "a" * 40},
            }
        FakeAPI.body = kwargs["body"]
        return {"html_url": "https://github.com/owner/repo/pull/7#pullrequestreview-1"}


def test_publish_uses_comment_event_and_bot_identity(git_repo: Path) -> None:
    subprocess.run(
        ["git", "remote", "add", "origin", "git@github.com:owner/repo.git"],
        cwd=git_repo,
        check=True,
    )
    with (
        patch("diffvouch.github_app.load_app", return_value=app_config()),
        patch("diffvouch.github_app.GitHubAPI", FakeAPI),
    ):
        url, bot = publish_review(review_result(), git_repo, None, 7)

    assert url.endswith("pullrequestreview-1")
    assert bot == "my-diffvouch[bot]"
    assert FakeAPI.body["event"] == "COMMENT"
    assert FakeAPI.body["commit_id"] == "b" * 40
    assert FakeAPI.body["comments"][0]["side"] == "RIGHT"


def test_publish_rejects_changed_pr_base(git_repo: Path) -> None:
    subprocess.run(
        ["git", "remote", "add", "origin", "git@github.com:owner/repo.git"],
        cwd=git_repo,
        check=True,
    )
    result = review_result()
    result.scope.base_sha = "c" * 40
    with (
        patch("diffvouch.github_app.load_app", return_value=app_config()),
        patch("diffvouch.github_app.GitHubAPI", FakeAPI),
        pytest.raises(GitHubError, match="PR base changed"),
    ):
        publish_review(result, git_repo, None, 7)
