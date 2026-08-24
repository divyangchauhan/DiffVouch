# DiffVouch

DiffVouch is a local-first CLI and portable Agent Skill for reviewing Git diffs
and GitHub pull requests. It reports blocking and non-blocking findings, explains
recommended fixes, and gives each change a transparent rating out of 5.

DiffVouch can use an existing Codex or Claude Code subscription through the
provider's installed CLI. API use is separate and explicit. Source code is sent
directly to the selected provider; DiffVouch has no hosted review service.

## Install the CLI

DiffVouch requires Python 3.11 or newer and Git. Install the current GitHub
version with `pipx`:

```bash
pipx install git+https://github.com/divyangchauhan/DiffVouch.git
```

Or with `uv`:

```bash
uv tool install git+https://github.com/divyangchauhan/DiffVouch.git
```

Confirm the installation:

```bash
diffvouch --version
```

## Configure an AI provider

Use a ChatGPT/Codex subscription:

```bash
diffvouch auth login openai
diffvouch auth status openai
```

Use a Claude subscription:

```bash
diffvouch auth login claude
diffvouch auth status claude
```

To use usage-based APIs instead, securely store a key:

```bash
diffvouch auth set-key openai
diffvouch auth set-key anthropic
```

Keys are entered through a hidden prompt. DiffVouch prefers the operating
system credential store and falls back to a mode-0600 user secret file when no
credential-store backend is available. API transport is never selected unless
you pass `--transport api`.

## Review changes

Review tracked and untracked working-tree changes with Codex:

```bash
diffvouch review --provider codex
```

Other common scopes:

```bash
diffvouch review --provider claude --staged-only
diffvouch review --provider codex --base main
diffvouch review --provider codex --base main --committed-only
diffvouch review --provider codex --transport api --model gpt-5.6-sol
diffvouch review --provider claude --format json --output review.json
```

Choose a model and reasoning effort when the selected provider supports them:

```bash
diffvouch review --provider codex --model gpt-5.6-sol --effort high
```

Use a review as a local quality gate:

```bash
diffvouch review --provider codex --fail-below 3.5
diffvouch review --provider codex --fail-on-severity high
```

## Publish reviews as your GitHub bot

DiffVouch does not operate a shared bot. Create a private GitHub App owned by
you or by the organization that owns the repositories it will review:

```bash
diffvouch github app create
```

Configure the app with:

- Pull requests: Read and write
- Contents: No access
- Webhooks: Disabled
- Subscribed events: None

Install the app on the selected repositories, download a private-key PEM, then
store and validate it locally:

```bash
diffvouch github app configure \
  --app-id 123456 \
  --slug my-diffvouch \
  --private-key ~/Downloads/my-diffvouch.pem

diffvouch github app status --repo owner/repository
```

For an organization repository, create the private app under that organization.
A private app created under a personal account cannot be installed on an
organization that does not own it.

Review and publish a checked-out pull request:

```bash
diffvouch review --provider codex --pr 123 --publish
```

Or discover the current branch's pull request from a local base comparison:

```bash
diffvouch review --provider claude --base main --publish
```

The review appears as `<app-slug>[bot]`. DiffVouch creates a repository-scoped
installation token on demand, keeps it only in memory, verifies that the live PR
base and head still match the reviewed commits, and posts one `COMMENT` review with
eligible inline comments. It never approves or requests changes.

For GitHub Enterprise Server, configure the app with `--host` and, when needed,
custom `--api-base-url` and `--web-base-url` values. Pass the same hostname to a
review with `--github-host` when it cannot be inferred from `origin`.

## Install the Agent Skill

The standalone skill remains available for testing DiffVouch's review workflow
inside Agent Skills-compatible coding agents:

```bash
npx skills add divyangchauhan/DiffVouch \
  --skill diffvouch-review \
  --global
```

Then ask your agent:

```text
Use $diffvouch-review to review my uncommitted changes.
```
