from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import urllib.error
import urllib.request
from abc import ABC, abstractmethod
from pathlib import Path
from typing import Any

from pydantic import ValidationError

from diffvouch.config import load_global_config
from diffvouch.credentials import SecretReference, read_secret
from diffvouch.errors import ArgumentError, ProviderError
from diffvouch.models import ProviderReview
from diffvouch.prompt import ReviewPrompt


def _run(
    command: list[str], *, cwd: Path, input_text: str | None = None
) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            command,
            cwd=cwd,
            input=input_text,
            text=True,
            capture_output=True,
            check=False,
        )
    except FileNotFoundError as exc:
        raise ProviderError(f"provider executable {command[0]!r} is not installed") from exc


def _validate(value: Any) -> ProviderReview:
    try:
        return ProviderReview.model_validate(value)
    except ValidationError as exc:
        raise ProviderError(f"provider returned an invalid review: {exc}") from exc


def _failure_detail(stderr: str, fallback: str) -> str:
    detail = stderr.strip()
    if not detail:
        return fallback
    error_at = detail.rfind("ERROR:")
    if error_at >= 0:
        detail = detail[error_at:]
    return detail[-4000:]


def _schema() -> dict[str, Any]:
    return ProviderReview.model_json_schema()


class Provider(ABC):
    name: str
    model: str

    @abstractmethod
    def review(self, prompt: ReviewPrompt) -> ProviderReview:
        raise NotImplementedError


class CodexCLIProvider(Provider):
    name = "codex"

    def __init__(self, model: str | None, effort: str | None) -> None:
        self.model = model or "default (Codex CLI)"
        self.requested_model = model
        self.effort = effort

    def review(self, prompt: ReviewPrompt) -> ProviderReview:
        if shutil.which("codex") is None:
            raise ProviderError("Codex CLI is not installed")
        with tempfile.TemporaryDirectory(prefix="diffvouch-codex-") as raw_directory:
            directory = Path(raw_directory)
            status = _run(["codex", "login", "status"], cwd=directory)
            if status.returncode != 0:
                raise ProviderError(
                    "Codex CLI is not authenticated; run 'diffvouch auth login openai'"
                )
            schema_path = directory / "review-schema.json"
            output_path = directory / "review.json"
            schema_path.write_text(json.dumps(_schema()), encoding="utf-8")
            command = [
                "codex",
                "exec",
                "--skip-git-repo-check",
                "--ephemeral",
                "--ignore-user-config",
                "--disable",
                "shell_tool",
                "--disable",
                "apps",
                "--disable",
                "multi_agent",
                "--config",
                'web_search="disabled"',
                "--config",
                f"developer_instructions={json.dumps(prompt.system)}",
                "--sandbox",
                "read-only",
                "--output-schema",
                str(schema_path),
                "--output-last-message",
                str(output_path),
            ]
            if self.requested_model:
                command.extend(["--model", self.requested_model])
            if self.effort:
                command.extend(["--config", f'model_reasoning_effort="{self.effort}"'])
            command.append("-")
            result = _run(command, cwd=directory, input_text=prompt.user)
            if result.returncode != 0:
                raise ProviderError(_failure_detail(result.stderr, "Codex review failed"))
            try:
                return _validate(json.loads(output_path.read_text(encoding="utf-8")))
            except (OSError, json.JSONDecodeError) as exc:
                raise ProviderError(f"Codex did not return valid structured JSON: {exc}") from exc


class ClaudeCLIProvider(Provider):
    name = "claude"

    def __init__(self, model: str | None, effort: str | None) -> None:
        self.model = model or "default (Claude Code)"
        self.requested_model = model
        self.effort = effort

    def review(self, prompt: ReviewPrompt) -> ProviderReview:
        if shutil.which("claude") is None:
            raise ProviderError("Claude Code CLI is not installed")
        with tempfile.TemporaryDirectory(prefix="diffvouch-claude-") as raw_directory:
            directory = Path(raw_directory)
            status = _run(["claude", "auth", "status"], cwd=directory)
            if status.returncode != 0:
                raise ProviderError(
                    "Claude Code is not authenticated; run 'diffvouch auth login claude'"
                )
            command = [
                "claude",
                "--safe-mode",
                "--tools",
                "",
                "--disallowedTools",
                "mcp__*",
                "--no-session-persistence",
                "--output-format",
                "json",
                "--json-schema",
                json.dumps(_schema(), separators=(",", ":")),
                "--system-prompt",
                prompt.system,
            ]
            if self.requested_model:
                command.extend(["--model", self.requested_model])
            if self.effort:
                command.extend(["--effort", self.effort])
            command.append("-p")
            result = _run(command, cwd=directory, input_text=prompt.user)
            if result.returncode != 0:
                raise ProviderError(_failure_detail(result.stderr, "Claude review failed"))
            try:
                envelope = json.loads(result.stdout)
                structured = envelope.get("structured_output")
                if structured is None and isinstance(envelope.get("result"), str):
                    structured = json.loads(envelope["result"])
                if structured is None:
                    raise ProviderError("Claude completed without a structured_output value")
                return _validate(structured)
            except json.JSONDecodeError as exc:
                raise ProviderError(f"Claude did not return valid structured JSON: {exc}") from exc


def _api_key(provider: str) -> str:
    environment_name = "OPENAI_API_KEY" if provider == "openai" else "ANTHROPIC_API_KEY"
    if value := os.environ.get(environment_name):
        return value
    entry = load_global_config().get("api_keys", {}).get(provider)
    if not isinstance(entry, dict):
        raise ProviderError(
            f"no {provider} API key configured; run 'diffvouch auth set-key {provider}'"
        )
    try:
        return read_secret(SecretReference.from_dict(entry))
    except (ArgumentError, KeyError, TypeError) as exc:
        raise ProviderError(f"cannot load stored {provider} API key: {exc}") from exc


def _post_json(url: str, headers: dict[str, str], body: dict[str, Any]) -> dict[str, Any]:
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", **headers},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=600) as response:
            return json.loads(response.read())
    except urllib.error.HTTPError as exc:
        detail = exc.read(4096).decode(errors="replace")
        raise ProviderError(f"provider API returned HTTP {exc.code}: {detail}") from exc
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        raise ProviderError(f"provider API request failed: {exc}") from exc


class OpenAIAPIProvider(Provider):
    name = "codex"

    def __init__(self, model: str | None, effort: str | None) -> None:
        if not model:
            raise ProviderError(
                "OpenAI API transport requires --model or provider.models.codex in .diffvouch.yml"
            )
        self.model = model
        self.effort = effort

    def review(self, prompt: ReviewPrompt) -> ProviderReview:
        body: dict[str, Any] = {
            "model": self.model,
            "input": [
                {"role": "developer", "content": prompt.system},
                {"role": "user", "content": prompt.user},
            ],
            "store": False,
            "text": {
                "format": {
                    "type": "json_schema",
                    "name": "diffvouch_review",
                    "schema": _schema(),
                    "strict": True,
                }
            },
        }
        if self.effort:
            body["reasoning"] = {"effort": self.effort}
        response = _post_json(
            "https://api.openai.com/v1/responses",
            {"Authorization": f"Bearer {_api_key('openai')}"},
            body,
        )
        if response.get("status") != "completed":
            raise ProviderError(
                f"OpenAI response did not complete: {response.get('status', 'unknown')}"
            )
        for item in response.get("output", []):
            if item.get("type") != "message":
                continue
            for content in item.get("content", []):
                if content.get("type") == "refusal":
                    raise ProviderError("OpenAI refused to produce the review")
                if content.get("type") == "output_text":
                    try:
                        return _validate(json.loads(content["text"]))
                    except json.JSONDecodeError as exc:
                        raise ProviderError(f"OpenAI returned invalid JSON: {exc}") from exc
        raise ProviderError("OpenAI response did not contain structured output")


class AnthropicAPIProvider(Provider):
    name = "claude"

    def __init__(self, model: str | None, effort: str | None) -> None:
        if not model:
            raise ProviderError(
                "Anthropic API transport requires --model or provider.models.claude in .diffvouch.yml"
            )
        self.model = model
        self.effort = effort

    def review(self, prompt: ReviewPrompt) -> ProviderReview:
        body: dict[str, Any] = {
            "model": self.model,
            "max_tokens": 8192,
            "system": prompt.system,
            "messages": [{"role": "user", "content": prompt.user}],
            "output_config": {"format": {"type": "json_schema", "schema": _schema()}},
        }
        if self.effort:
            body["effort"] = self.effort
        response = _post_json(
            "https://api.anthropic.com/v1/messages",
            {
                "x-api-key": _api_key("anthropic"),
                "anthropic-version": "2023-06-01",
            },
            body,
        )
        if response.get("stop_reason") == "max_tokens":
            raise ProviderError("Anthropic response was truncated")
        for content in response.get("content", []):
            if content.get("type") == "text":
                try:
                    return _validate(json.loads(content["text"]))
                except json.JSONDecodeError as exc:
                    raise ProviderError(f"Anthropic returned invalid JSON: {exc}") from exc
        raise ProviderError("Anthropic response did not contain structured output")


def create_provider(name: str, transport: str, model: str | None, effort: str | None) -> Provider:
    if name == "codex" and transport == "cli":
        return CodexCLIProvider(model, effort)
    if name == "codex" and transport == "api":
        return OpenAIAPIProvider(model, effort)
    if name == "claude" and transport == "cli":
        return ClaudeCLIProvider(model, effort)
    if name == "claude" and transport == "api":
        return AnthropicAPIProvider(model, effort)
    raise ProviderError(f"unsupported provider/transport combination: {name}/{transport}")
