from __future__ import annotations

import json
import subprocess
from pathlib import Path
from unittest.mock import patch

from diffvouch.prompt import ReviewPrompt
from diffvouch.providers import (
    AnthropicAPIProvider,
    ClaudeCLIProvider,
    CodexCLIProvider,
    OpenAIAPIProvider,
)

REVIEW = {
    "summary": "No issues.",
    "dimensions": {
        "correctness": 5,
        "security": 5,
        "maintainability": 5,
        "testing": 5,
        "scope": 5,
    },
    "findings": [],
    "positiveObservations": ["Focused change."],
    "needsVerification": [],
}
PROMPT = ReviewPrompt(system="review policy", user="review prompt")


def completed(command: list[str], stdout: str = "") -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")


def test_codex_cli_is_ephemeral_read_only_and_schema_constrained() -> None:
    commands: list[list[str]] = []

    def fake_run(command: list[str], *, cwd: Path, input_text: str | None = None):
        commands.append(command)
        if command[:3] == ["codex", "login", "status"]:
            return completed(command)
        output = Path(command[command.index("--output-last-message") + 1])
        output.write_text(json.dumps(REVIEW), encoding="utf-8")
        assert input_text == "review prompt"
        return completed(command)

    with (
        patch("diffvouch.providers.shutil.which", return_value="/usr/bin/codex"),
        patch("diffvouch.providers._run", side_effect=fake_run),
    ):
        result = CodexCLIProvider("gpt-test", "high").review(PROMPT)

    command = commands[1]
    assert "--ephemeral" in command
    assert "--ignore-user-config" in command
    assert command.count("--disable") == 3
    assert "shell_tool" in command
    assert 'web_search="disabled"' in command
    assert any(item.startswith("developer_instructions=") for item in command)
    assert command[command.index("--sandbox") + 1] == "read-only"
    assert "--output-schema" in command
    assert command[command.index("--model") + 1] == "gpt-test"
    assert result.summary == "No issues."


def test_claude_cli_disables_tools_and_uses_structured_output() -> None:
    commands: list[list[str]] = []

    def fake_run(command: list[str], *, cwd: Path, input_text: str | None = None):
        commands.append(command)
        if command[:3] == ["claude", "auth", "status"]:
            return completed(command)
        assert input_text == "review prompt"
        assert "review prompt" not in command
        return completed(command, json.dumps({"structured_output": REVIEW}))

    with (
        patch("diffvouch.providers.shutil.which", return_value="/usr/bin/claude"),
        patch("diffvouch.providers._run", side_effect=fake_run),
    ):
        result = ClaudeCLIProvider("claude-test", "xhigh").review(PROMPT)

    command = commands[1]
    assert command[command.index("--tools") + 1] == ""
    assert command[command.index("--disallowedTools") + 1] == "mcp__*"
    assert "--no-session-persistence" in command
    assert "--json-schema" in command
    assert command[command.index("--system-prompt") + 1] == "review policy"
    assert result.summary == "No issues."


def test_openai_api_is_explicit_structured_and_not_stored() -> None:
    with (
        patch("diffvouch.providers._api_key", return_value="secret"),
        patch(
            "diffvouch.providers._post_json",
            return_value={
                "status": "completed",
                "output": [
                    {
                        "type": "message",
                        "content": [{"type": "output_text", "text": json.dumps(REVIEW)}],
                    }
                ],
            },
        ) as post,
    ):
        OpenAIAPIProvider("gpt-test", "high").review(PROMPT)

    body = post.call_args.args[2]
    assert body["store"] is False
    assert body["text"]["format"]["strict"] is True
    assert body["reasoning"] == {"effort": "high"}
    assert body["input"][0] == {"role": "developer", "content": "review policy"}
    assert body["input"][1] == {"role": "user", "content": "review prompt"}


def test_anthropic_api_uses_json_schema() -> None:
    with (
        patch("diffvouch.providers._api_key", return_value="secret"),
        patch(
            "diffvouch.providers._post_json",
            return_value={"content": [{"type": "text", "text": json.dumps(REVIEW)}]},
        ) as post,
    ):
        AnthropicAPIProvider("claude-test", None).review(PROMPT)

    body = post.call_args.args[2]
    assert body["output_config"]["format"]["type"] == "json_schema"
    assert body["system"] == "review policy"
    assert body["messages"][0]["content"] == "review prompt"
