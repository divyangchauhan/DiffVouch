# Harness compatibility

DiffVouch keeps one canonical skill under `.agents/skills/diffvouch-review` and
uses small discovery wrappers only where a harness requires its own project
directory. The workflow itself contains no provider-specific tool calls.

| Harness | Project discovery path | Repository setup |
|---|---|---|
| Codex | `.agents/skills` | Canonical skill, native discovery |
| Claude Code | `.claude/skills` | Thin wrapper that loads the canonical skill |
| Cursor | `.cursor/skills` | Thin wrapper that loads the canonical skill |
| Gemini CLI | `.agents/skills` alias | Canonical skill, native discovery |
| OpenCode | `.agents/skills` compatibility path | Canonical skill, native discovery |
| GitHub Copilot in VS Code | `.agents/skills` | Canonical skill, native discovery |

Any other harness implementing the Agent Skills specification can load or copy
the canonical skill directory. The harness must provide:

- Read-only Git and filesystem access for review generation.
- A fresh process or subagent mechanism to satisfy isolated reviews.
- Model and reasoning-effort controls when the user explicitly selects them.
- Python 3 and authenticated `gh` only when GitHub publication is requested.

If a harness cannot apply an explicit model, effort, isolation, or publication
requirement, the skill stops instead of silently weakening the request.
