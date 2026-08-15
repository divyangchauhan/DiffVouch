# DiffVouch

DiffVouch is a portable Agent Skill for reviewing Git changes. It reviews the
requested diff, reports actionable findings, and gives the change a transparent
rating out of 5.

## Current capabilities

- Review tracked and untracked working-tree changes.
- Review staged changes only.
- Review committed branch changes against `main` or another base branch.
- Review a GitHub pull request.
- Run the review in an isolated subagent or fresh process when the harness
  supports it.
- Use an explicitly requested model and reasoning-effort level when the harness
  supports those controls.
- Publish one comment-only GitHub review, including eligible inline comments,
  only when explicitly requested.
- Fail without a rating when the complete patch cannot be captured. The starter
  skill currently accepts patches up to 500,000 bytes by default.

DiffVouch never approves a pull request or formally requests changes. GitHub
publication always uses the `COMMENT` review event.

## Requirements

- Git
- Python 3
- An Agent Skills-compatible coding agent
- GitHub CLI (`gh`) authenticated with pull-request write access when publishing
  a review

## Install

The recommended method is the [Vercel Skills CLI](https://github.com/vercel-labs/skills).
Install DiffVouch globally to make it available in every repository:

```bash
npx skills add divyangchauhan/DiffVouch \
  --skill diffvouch-review \
  --global
```

The interactive installer lets you select the coding agents that should receive
the skill. For a non-interactive Codex installation:

```bash
npx skills add divyangchauhan/DiffVouch \
  --skill diffvouch-review \
  --global \
  --agent codex \
  --yes
```

Select multiple harnesses by repeating `--agent`:

```bash
npx skills add divyangchauhan/DiffVouch \
  --skill diffvouch-review \
  --global \
  --agent codex \
  --agent claude-code \
  --agent cursor \
  --yes
```

Confirm the global installation:

```bash
npx skills ls --global
```

To use DiffVouch for one session without installing it:

```bash
npx skills use divyangchauhan/DiffVouch@diffvouch-review --agent claude-code
```

For a manual installation, copy or symlink the complete
`skills/diffvouch-review` directory into a global skill directory supported by
your agent. The complete directory is required because the skill uses bundled
scripts and references.

## Use

Ask your coding agent to use `$diffvouch-review`. For example:

```text
Use $diffvouch-review to review my uncommitted changes.
Use $diffvouch-review to review my staged changes.
Use $diffvouch-review to review committed changes against main.
Use $diffvouch-review to review this PR.
Use $diffvouch-review with model=<model-id> effort=high to review this PR.
Use $diffvouch-review to review this PR and publish the review to GitHub.
```

If you do not specify a scope, DiffVouch reviews the current working-tree
changes. Nothing is posted to GitHub unless the current request explicitly asks
for publication.
