# Harness compatibility

DiffVouch is distributed as one canonical Agent Skills package under
`skills/diffvouch-review`. Install it into a harness-specific global directory;
do not copy executable review code into the repository being reviewed.

The `skills` CLI selects the correct destination for Codex, Claude Code,
Cursor, Gemini CLI, OpenCode, GitHub Copilot, and other supported agents. The
workflow itself contains no provider-specific tool calls.

The harness must provide:

- Read-only Git and filesystem access for review generation.
- A fresh process or subagent mechanism to satisfy isolated reviews.
- Model and reasoning-effort controls when the user explicitly selects them.
- Python 3 and authenticated `gh` only when GitHub publication is requested.

If a harness cannot apply an explicit model, effort, isolation, or publication
requirement, stop instead of silently weakening the request.

For manual installation, copy or symlink the complete
`skills/diffvouch-review` directory into a skill location documented by the
target harness. Preserve `scripts/`, `references/`, `tests/`, and
`agents/openai.yaml`; installing only `SKILL.md` is incomplete.
