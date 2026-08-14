# DiffVouch

DiffVouch is a portable Agent Skill that reviews Git working-tree, staged, or
branch changes, reports actionable findings, and assigns a transparent rating
out of 5. When explicitly requested, it can publish one comment-only GitHub PR
review with eligible inline comments.

The repository is a skill source, not a project-local installation. The
canonical package is [`skills/diffvouch-review`](skills/diffvouch-review).

## Requirements

- Git
- Python 3
- An Agent Skills-compatible coding agent
- GitHub CLI (`gh`) authenticated with pull-request write access only when
  publishing a review

## Install with the skills CLI

Install globally so the skill can review any repository without executing a
copy controlled by the target branch:

```bash
npx skills add divyangchauhan/DiffLoom --skill diffvouch-review --global
```

Install non-interactively for selected agents:

```bash
npx skills add divyangchauhan/DiffLoom \
  --skill diffvouch-review \
  --global \
  --agent codex \
  --agent claude-code \
  --agent cursor \
  --agent gemini-cli \
  --agent opencode \
  --yes
```

List the skill without installing it:

```bash
npx skills add divyangchauhan/DiffLoom --list
```

Use it for one session without installing:

```bash
npx skills use divyangchauhan/DiffLoom@diffvouch-review --agent claude-code
```

Update an existing installation:

```bash
npx skills update diffvouch-review --global
```

## Install manually

Clone the repository into a trusted location, then copy or symlink the complete
`skills/diffvouch-review` directory into the global skill directory documented
by your agent. Do not install only `SKILL.md`; the skill also requires its
scripts and references.

For Codex, you can also ask the built-in installer:

```text
$skill-installer Install diffvouch-review from
https://github.com/divyangchauhan/DiffLoom/tree/main/skills/diffvouch-review
```

## Use

Examples:

```text
Use $diffvouch-review to review my uncommitted changes.
Use $diffvouch-review to review committed changes against main.
Use $diffvouch-review with model=<model-id> effort=high to review this PR.
Use $diffvouch-review to review this PR and publish the review to GitHub.
```

Nothing is published unless the current request explicitly asks for it. GitHub
publication always uses the `COMMENT` event; the skill never approves a PR or
formally requests changes.

## Repository layout

```text
skills/
└── diffvouch-review/
    ├── SKILL.md
    ├── agents/openai.yaml
    ├── references/
    ├── scripts/
    └── tests/
```

## Validate locally

```bash
python3 -m unittest discover -s skills/diffvouch-review/tests -v
npx skills add . --list
```

The product direction and future CLI requirements are documented in
[`PRD.md`](PRD.md).
