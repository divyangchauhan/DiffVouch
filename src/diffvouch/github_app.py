from __future__ import annotations

import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request
import webbrowser
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import jwt

from diffvouch.config import load_global_config, save_global_config
from diffvouch.credentials import SecretReference, delete_secret, read_secret, store_secret
from diffvouch.errors import ArgumentError, GitError, GitHubError
from diffvouch.git import run_git
from diffvouch.models import Confidence, Finding, ReviewResult


@dataclass(frozen=True)
class GitHubAppConfig:
    app_id: str
    slug: str
    host: str
    api_base_url: str
    web_base_url: str
    api_version: str | None
    private_key: SecretReference

    @classmethod
    def from_dict(cls, value: dict[str, Any]) -> GitHubAppConfig:
        try:
            return cls(
                app_id=str(value["app_id"]),
                slug=value["slug"],
                host=value["host"],
                api_base_url=value["api_base_url"].rstrip("/"),
                web_base_url=value["web_base_url"].rstrip("/"),
                api_version=value.get("api_version"),
                private_key=SecretReference.from_dict(value["private_key"]),
            )
        except (KeyError, TypeError) as exc:
            raise GitHubError("stored GitHub App configuration is invalid") from exc

    def as_dict(self) -> dict[str, Any]:
        return {
            "app_id": self.app_id,
            "slug": self.slug,
            "host": self.host,
            "api_base_url": self.api_base_url,
            "web_base_url": self.web_base_url,
            "api_version": self.api_version,
            "private_key": self.private_key.as_dict(),
        }


def default_urls(host: str) -> tuple[str, str, str | None]:
    if host == "github.com":
        return "https://api.github.com", "https://github.com", "2026-03-10"
    return f"https://{host}/api/v3", f"https://{host}", None


def creation_url(host: str, owner: str | None = None) -> str:
    _, web, _ = default_urls(host)
    if owner:
        return f"{web}/organizations/{urllib.parse.quote(owner)}/settings/apps/new"
    return f"{web}/settings/apps/new"


def open_creation_page(host: str, owner: str | None = None) -> str:
    url = creation_url(host, owner)
    webbrowser.open(url)
    return url


class GitHubAPI:
    def __init__(self, config: GitHubAppConfig, private_key: str | None = None) -> None:
        self.config = config
        self._private_key = private_key

    @property
    def private_key(self) -> str:
        if self._private_key is None:
            self._private_key = read_secret(self.config.private_key)
        return self._private_key

    def jwt(self) -> str:
        now = int(time.time())
        try:
            return jwt.encode(
                {"iat": now - 60, "exp": now + 540, "iss": self.config.app_id},
                self.private_key,
                algorithm="RS256",
            )
        except Exception as exc:
            raise GitHubError(
                "the configured GitHub App private key cannot sign RS256 JWTs"
            ) from exc

    def _headers(self, token: str, accept: str) -> dict[str, str]:
        headers = {
            "Accept": accept,
            "Authorization": f"Bearer {token}",
            "User-Agent": "DiffVouch/0.1",
        }
        if self.config.api_version:
            headers["X-GitHub-Api-Version"] = self.config.api_version
        return headers

    def request(
        self,
        method: str,
        path: str,
        *,
        token: str,
        body: dict[str, Any] | None = None,
        accept: str = "application/vnd.github+json",
        raw: bool = False,
    ) -> Any:
        url = f"{self.config.api_base_url}{path}"
        request = urllib.request.Request(
            url,
            data=json.dumps(body).encode() if body is not None else None,
            headers={"Content-Type": "application/json", **self._headers(token, accept)},
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                payload = response.read()
                return payload.decode(errors="replace") if raw else json.loads(payload or b"{}")
        except urllib.error.HTTPError as exc:
            detail = exc.read(4096).decode(errors="replace")
            raise GitHubError(
                f"GitHub API {method} {path} returned HTTP {exc.code}: {detail}"
            ) from exc
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise GitHubError(f"GitHub API {method} {path} failed: {exc}") from exc

    def app(self) -> dict[str, Any]:
        return self.request("GET", "/app", token=self.jwt())

    def installation_id(self, owner: str, repository: str) -> int:
        value = self.request(
            "GET",
            f"/repos/{urllib.parse.quote(owner)}/{urllib.parse.quote(repository)}/installation",
            token=self.jwt(),
        )
        try:
            return int(value["id"])
        except (KeyError, TypeError, ValueError) as exc:
            raise GitHubError(
                "GitHub did not return an installation ID for this repository"
            ) from exc

    def installation_token(self, owner: str, repository: str) -> str:
        installation_id = self.installation_id(owner, repository)
        value = self.request(
            "POST",
            f"/app/installations/{installation_id}/access_tokens",
            token=self.jwt(),
            body={
                "repositories": [repository],
                "permissions": {"pull_requests": "write"},
            },
        )
        try:
            return value["token"]
        except (KeyError, TypeError) as exc:
            raise GitHubError("GitHub did not return an installation access token") from exc


def configure_app(
    *,
    app_id: str,
    slug: str,
    private_key_path: Path,
    host: str,
    storage: str,
    api_base_url: str | None = None,
    web_base_url: str | None = None,
    api_version: str | None = None,
) -> GitHubAppConfig:
    if not re.fullmatch(r"[0-9]+", app_id):
        raise ArgumentError("GitHub App ID must be numeric")
    if not re.fullmatch(r"[a-z0-9-]+", slug):
        raise ArgumentError(
            "GitHub App slug must contain only lowercase letters, digits, and hyphens"
        )
    try:
        private_key = private_key_path.expanduser().read_text(encoding="utf-8")
    except OSError as exc:
        raise ArgumentError(f"cannot read private key {private_key_path}: {exc}") from exc
    default_api, default_web, default_version = default_urls(host)
    provisional = GitHubAppConfig(
        app_id=app_id,
        slug=slug,
        host=host,
        api_base_url=(api_base_url or default_api).rstrip("/"),
        web_base_url=(web_base_url or default_web).rstrip("/"),
        api_version=api_version if api_version is not None else default_version,
        private_key=SecretReference(name="provisional", backend="file"),
    )
    app = GitHubAPI(provisional, private_key=private_key).app()
    if str(app.get("id")) != str(app_id):
        raise GitHubError(
            f"authenticated app ID {app.get('id')} does not match configured ID {app_id}"
        )
    actual_slug = app.get("slug")
    if actual_slug and actual_slug != slug:
        raise GitHubError(f"authenticated app slug {actual_slug!r} does not match {slug!r}")

    global_config = load_global_config()
    old = global_config["github_apps"].get(host)
    reference = store_secret(f"github-app:{host}:{app_id}", private_key, storage)
    configured = GitHubAppConfig(
        app_id=app_id,
        slug=slug,
        host=host,
        api_base_url=provisional.api_base_url,
        web_base_url=provisional.web_base_url,
        api_version=provisional.api_version,
        private_key=reference,
    )
    global_config["github_apps"][host] = configured.as_dict()
    save_global_config(global_config)
    if isinstance(old, dict) and isinstance(old.get("private_key"), dict):
        old_reference = SecretReference.from_dict(old["private_key"])
        if old_reference != reference:
            delete_secret(old_reference)
    return configured


def load_app(host: str) -> GitHubAppConfig:
    value = load_global_config().get("github_apps", {}).get(host)
    if not isinstance(value, dict):
        raise GitHubError(
            f"no GitHub App configured for {host}; run 'diffvouch github app configure'"
        )
    return GitHubAppConfig.from_dict(value)


def remove_app(host: str) -> bool:
    global_config = load_global_config()
    value = global_config.get("github_apps", {}).pop(host, None)
    if not isinstance(value, dict):
        return False
    if isinstance(value.get("private_key"), dict):
        delete_secret(SecretReference.from_dict(value["private_key"]))
    save_global_config(global_config)
    return True


def parse_remote(
    repository: Path,
    explicit: str | None = None,
    explicit_host: str | None = None,
) -> tuple[str, str, str]:
    if explicit and not re.fullmatch(r"[^/\s]+/[^/\s]+", explicit):
        raise GitHubError("--repo must be owner/name")
    try:
        remote = run_git(repository, "remote", "get-url", "origin").strip()
    except GitError:
        if explicit:
            owner, name = explicit.split("/", 1)
            return explicit_host or "github.com", owner, name
        raise
    patterns = [
        r"git@(?P<host>[^:]+):(?P<owner>[^/]+)/(?P<repo>[^/]+)$",
        r"ssh://git@(?P<host>[^/]+)/(?P<owner>[^/]+)/(?P<repo>[^/]+)$",
        r"https?://(?P<host>[^/]+)/(?P<owner>[^/]+)/(?P<repo>[^/]+)$",
    ]
    for pattern in patterns:
        match = re.fullmatch(pattern, remote)
        if match:
            owner, name = match.group("owner"), match.group("repo").removesuffix(".git")
            if explicit:
                owner, name = explicit.split("/", 1)
            return explicit_host or match.group("host"), owner, name
    if explicit:
        owner, name = explicit.split("/", 1)
        return explicit_host or "github.com", owner, name
    raise GitHubError("cannot resolve GitHub owner/repository from origin; pass --repo owner/name")


def discover_pull_request(
    api: GitHubAPI,
    token: str,
    repository: Path,
    owner: str,
    name: str,
    explicit: int | None,
) -> dict[str, Any]:
    if explicit is not None:
        return api.request("GET", f"/repos/{owner}/{name}/pulls/{explicit}", token=token)
    branch = run_git(repository, "symbolic-ref", "--quiet", "--short", "HEAD").strip()
    if not branch:
        raise GitHubError("detached HEAD requires --pr")
    query = urllib.parse.urlencode({"state": "open", "per_page": 100})
    pulls = api.request("GET", f"/repos/{owner}/{name}/pulls?{query}", token=token)
    local_head = run_git(repository, "rev-parse", "HEAD").strip()
    matches = (
        [
            pull
            for pull in pulls
            if isinstance(pull, dict)
            and pull.get("head", {}).get("ref") == branch
            and pull.get("head", {}).get("sha") == local_head
        ]
        if isinstance(pulls, list)
        else []
    )
    if len(matches) != 1:
        raise GitHubError("could not resolve exactly one open PR for the current branch; pass --pr")
    return matches[0]


DiffLocation = tuple[str, str, int]


def changed_lines(diff: str) -> set[DiffLocation]:
    changed: set[DiffLocation] = set()
    old_path: str | None = None
    new_path: str | None = None
    old_line = new_line = 0
    in_hunk = False
    for raw_line in diff.splitlines():
        if raw_line.startswith("diff --git "):
            old_path = new_path = None
            in_hunk = False
        elif not in_hunk and raw_line.startswith("--- "):
            old_path = _decode_diff_path(raw_line[4:])
        elif not in_hunk and raw_line.startswith("+++ "):
            new_path = _decode_diff_path(raw_line[4:])
        elif raw_line.startswith("@@ "):
            match = re.match(r"@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@", raw_line)
            if match:
                old_line, new_line = map(int, match.groups())
                in_hunk = True
        elif in_hunk and raw_line.startswith("+") and new_path:
            changed.add((new_path, "RIGHT", new_line))
            new_line += 1
        elif in_hunk and raw_line.startswith("-") and (new_path or old_path):
            changed.add((new_path or old_path or "", "LEFT", old_line))
            old_line += 1
        elif in_hunk and raw_line.startswith(" "):
            old_line += 1
            new_line += 1
    return changed


def _decode_diff_path(value: str) -> str | None:
    if value == "/dev/null":
        return None
    if value.startswith('"') and value.endswith('"'):
        encoded = bytearray()
        payload = value[1:-1]
        index = 0
        escapes = {"a": 7, "b": 8, "f": 12, "n": 10, "r": 13, "t": 9, "v": 11, "\\": 92, '"': 34}
        while index < len(payload):
            character = payload[index]
            if character != "\\":
                encoded.extend(character.encode("utf-8"))
                index += 1
                continue
            index += 1
            if index >= len(payload):
                raise GitHubError("invalid trailing escape in Git diff path")
            escaped = payload[index]
            if escaped in escapes:
                encoded.append(escapes[escaped])
                index += 1
            elif escaped in "01234567":
                end = index + 1
                while end < min(index + 3, len(payload)) and payload[end] in "01234567":
                    end += 1
                encoded.append(int(payload[index:end], 8))
                index = end
            else:
                encoded.extend(escaped.encode("utf-8"))
                index += 1
        value = encoded.decode("utf-8", errors="surrogateescape")
    return value[2:] if value.startswith(("a/", "b/")) else value


def _finding_markdown(finding: Finding) -> str:
    marker = "Blocking" if finding.blocking else "Non-blocking"
    location = f" `{finding.path}:{finding.line}`" if finding.path and finding.line else ""
    return (
        f"- **{marker} · {finding.severity.value} · {finding.title}**{location}\n"
        f"  {finding.explanation}\n"
        f"  **Recommendation:** {finding.recommendation}"
    )


def review_body(result: ReviewResult) -> str:
    blocking = [item for item in result.findings if item.blocking]
    non_blocking = [item for item in result.findings if not item.blocking]
    lines = [
        "## DiffVouch review",
        "",
        f"**Rating: {result.rating.overall:.1f}/5 — {result.rating.label}**",
        "",
        result.summary,
        "",
        "### Blocking issues",
        "",
        *([_finding_markdown(item) for item in blocking] or ["None."]),
        "",
        "### Non-blocking issues",
        "",
        *([_finding_markdown(item) for item in non_blocking] or ["None."]),
        "",
        "### Rating breakdown",
        "",
        "| Dimension | Score |",
        "|---|---:|",
    ]
    lines.extend(
        f"| {name.title()} | {score:.1f}/5 |"
        for name, score in result.rating.dimensions.model_dump().items()
    )
    lines.extend(
        [
            "",
            (
                f"Provider: `{result.provider.name}/{result.provider.transport}` · "
                f"Model: `{result.provider.model}` · Reviewed commit: `{result.scope.head_sha}`"
            ),
        ]
    )
    return "\n".join(lines)


def publish_review(
    result: ReviewResult,
    repository: Path,
    explicit_repo: str | None,
    explicit_pr: int | None,
    explicit_host: str | None = None,
) -> tuple[str, str]:
    if result.partial or result.status != "complete":
        raise GitHubError("only complete reviews may be published")
    host, owner, name = parse_remote(repository, explicit_repo, explicit_host)
    config = load_app(host)
    api = GitHubAPI(config)
    token = api.installation_token(owner, name)
    pull = discover_pull_request(api, token, repository, owner, name, explicit_pr)
    if pull.get("state") != "open":
        raise GitHubError("pull request is not open")
    live_head = pull.get("head", {}).get("sha")
    live_base = pull.get("base", {}).get("sha")
    if live_head != result.scope.head_sha:
        raise GitHubError(
            f"PR head changed: reviewed {result.scope.head_sha}, current {live_head}; run a fresh review"
        )
    if live_base != result.scope.base_sha:
        raise GitHubError(
            f"PR base changed: reviewed {result.scope.base_sha}, current {live_base}; run a fresh review"
        )
    number = int(pull["number"])
    live_diff = api.request(
        "GET",
        f"/repos/{owner}/{name}/pulls/{number}",
        token=token,
        accept="application/vnd.github.v3.diff",
        raw=True,
    )
    eligible = changed_lines(live_diff)
    comments: list[dict[str, Any]] = []
    for finding in result.findings:
        if (
            finding.confidence is Confidence.LOW
            or not finding.path
            or not finding.line
            or not finding.side
        ):
            continue
        side = "RIGHT" if finding.side == "new" else "LEFT"
        if (finding.path, side, finding.line) not in eligible:
            continue
        body = f"**{finding.title}**\n\n{finding.explanation}\n\n**Recommendation:** {finding.recommendation}"
        if finding.confidence is Confidence.MEDIUM:
            body = "**Medium confidence:** " + body
        comments.append({"path": finding.path, "line": finding.line, "side": side, "body": body})
    response = api.request(
        "POST",
        f"/repos/{owner}/{name}/pulls/{number}/reviews",
        token=token,
        body={
            "commit_id": result.scope.head_sha,
            "event": "COMMENT",
            "body": review_body(result),
            "comments": comments,
        },
    )
    url = response.get("html_url")
    if not isinstance(url, str):
        raise GitHubError("GitHub created the review but did not return its URL")
    return url, f"{config.slug}[bot]"


def resolve_pull_request(
    repository: Path,
    explicit_repo: str | None,
    explicit_pr: int | None,
    explicit_host: str | None = None,
) -> tuple[str, dict[str, Any]]:
    host, owner, name = parse_remote(repository, explicit_repo, explicit_host)
    config = load_app(host)
    api = GitHubAPI(config)
    token = api.installation_token(owner, name)
    pull = discover_pull_request(api, token, repository, owner, name, explicit_pr)
    return f"{owner}/{name}", pull
