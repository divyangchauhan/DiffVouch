# DiffVouch

DiffVouch is a local-first Go CLI and portable Agent Skill for reviewing Git
diffs and GitHub pull requests. It reports blocking and non-blocking findings,
explains recommended fixes, and gives each change a transparent rating out of 5.

The CLI is distributed as a standalone native executable. Users do not need Go,
Python, Node.js, or a DiffVouch-hosted service to run it. Git is required. Codex
or Claude Code is required only when using the corresponding subscription; API
transport talks directly to OpenAI or Anthropic.

## Install the CLI

Download the archive for your operating system and architecture from
[GitHub Releases](https://github.com/divyangchauhan/DiffVouch/releases), verify
it against `checksums.txt`, and place `diffvouch` (or `diffvouch.exe`) on your
`PATH`.

Developers with Go installed can install from source:

```bash
go install github.com/divyangchauhan/DiffVouch/cmd/diffvouch@latest
```

Confirm the installation:

```bash
diffvouch --version
diffvouch --help
```

Tagged releases are built for macOS, Linux, and Windows on AMD64 and ARM64.

## Configure an AI provider

Use an existing ChatGPT/Codex subscription:

```bash
diffvouch auth login openai
diffvouch auth status openai
```

Use an existing Claude subscription:

```bash
diffvouch auth login claude
diffvouch auth status claude
```

To use usage-based APIs instead, securely store a key:

```bash
diffvouch auth set-key openai
diffvouch auth set-key anthropic
```

DiffVouch prefers the operating-system credential manager and falls back to a
mode-0600 user secret file when no keyring backend is available. API transport
is never selected unless the user passes `--transport api`.

## Review changes

Review tracked and untracked working-tree changes:

```bash
diffvouch review --provider codex
```

Other common scopes:

```bash
diffvouch review --provider claude --staged-only
diffvouch review --provider codex --base main
diffvouch review --provider codex --base main --committed-only
diffvouch review --provider codex --model gpt-5.6-sol --effort xhigh
diffvouch review --provider codex --transport api --model gpt-5.6-sol
diffvouch review --provider claude --format json --output review.json
```

Use the result as a local quality gate:

```bash
diffvouch review --provider codex --fail-below 3.5
diffvouch review --provider codex --fail-on-severity high
```

Repository-specific rules can be committed in `.diffvouch.yml`. DiffVouch reads
that policy from the trusted base commit so a change cannot suppress its own
review. A repository cannot select billable API transport or enable publishing.

### Customize the review prompt for one run

Replace DiffVouch's default review guidance inline or from a file:

```bash
diffvouch review --provider codex \
  --prompt 'Focus on backward compatibility and database migration safety.'

diffvouch review --provider claude --prompt-file ./review-policy.md
printf '%s\n' 'Review only authentication and authorization regressions.' | \
  diffvouch review --provider codex --prompt-file -
```

`--prompt` and `--prompt-file` are mutually exclusive and apply only to the
current invocation. They replace the embedded review guidance, while DiffVouch
always retains its safety, patch-scope, evidence, severity, scoring, and JSON
output contract. Custom prompts therefore cannot make patch contents trusted or
remove the structured response requirements. Prompt input is limited to 256 KiB.

For durable project rules, prefer `review.instructions` in `.diffvouch.yml`.
Those instructions are layered onto the prompt from the trusted comparison
revision. The embedded default is available at
[`internal/review/prompts/default.md`](internal/review/prompts/default.md), and
the research behind it is in
[`docs/code-review-prompt-research.md`](docs/code-review-prompt-research.md).

## Publish reviews as your GitHub bot

DiffVouch does not operate a shared bot. Create a private GitHub App owned by
you or by the organization that owns the repositories it will review:

```bash
diffvouch github app create
# For an organization-owned app:
diffvouch github app create --owner your-organization
```

The command starts a temporary localhost callback and uses GitHub's App Manifest
flow. It opens a preconfigured GitHub confirmation page with only:

- Pull requests: Read and write
- Contents: No access
- Webhooks: Disabled
- Subscribed events: None

After you confirm the generated name, DiffVouch exchanges the one-time manifest
code, validates the generated credentials, stores the private key securely, and
opens the repository installation page. You do not download a PEM or enter an
App ID. Then verify access:

```bash
diffvouch github app status --repo owner/repository
```

On a headless machine, pass `--no-browser` and open the printed localhost URL
through a forwarded port. If the browser cannot reach the localhost callback, copy
the `code` query parameter from the failed redirect and finish with
`diffvouch github app create --code CODE`. The manual `github app configure`
command remains available for an existing app.

Review and publish a checked-out pull request:

```bash
diffvouch review --provider codex --pr 123 --publish
```

Or discover the current branch's pull request from a base comparison:

```bash
diffvouch review --provider claude --base main --publish
```

Reviews appear as `<app-slug>[bot]`. DiffVouch creates a repository-restricted
installation token on demand, keeps it only in memory, revalidates the live PR
base and head, and posts exactly one `COMMENT` review with eligible inline
comments. It never approves or requests changes.

GitHub Enterprise Server is supported through `--host`, `--api-base-url`,
`--web-base-url`, and `--github-host` where its version supports App manifests.

## Install the Agent Skill

The standalone Agent Skill remains available for Codex, Claude Code, Cursor,
and other Agent Skills-compatible harnesses:

```bash
npx skills add divyangchauhan/DiffVouch \
  --skill diffvouch-review \
  --global
```

Then ask your agent:

```text
Use $diffvouch-review to review my uncommitted changes.
```

## Build and test

```bash
go test ./...
go vet ./...
go build -trimpath ./cmd/diffvouch
```

GoReleaser packages versioned standalone binaries when a `v*` tag is pushed.
