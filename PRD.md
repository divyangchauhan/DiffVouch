# DiffVouch Product Requirements Document

## 1. Product Summary

DiffVouch is a local-first CLI application that reviews Git changes using the developer's existing Codex or Claude CLI subscription. It can also use provider API keys when explicitly requested.

It reviews:

- All uncommitted changes by default.
- Committed branch changes relative to `main` or another specified base branch.
- Both committed and uncommitted work when reviewing against a base branch.

DiffVouch produces actionable findings and a transparent rating out of 5. Results appear locally by default and can be published to a matching GitHub pull request only through an explicit `--publish` flag.

## 2. Target User

The MVP is optimized for individual developers who want feedback before committing changes or requesting human review.

Secondary future audiences include:

- Small engineering teams standardizing review criteria.
- Open-source maintainers.
- CI pipelines enforcing quality thresholds.

## 3. Product Goals

- Find correctness, security, maintainability, testing, and scope problems before human review.
- Work with existing Codex and Claude CLI authentication.
- Avoid requiring source code to pass through a DiffVouch-hosted service.
- Support local work, branch-level review, and GitHub pull requests through one workflow.
- Produce consistent, explainable ratings instead of an opaque score.
- Make repository-specific review standards version-controllable.
- Remain informational by default, with optional quality-gate behavior.

## 4. Non-Goals for MVP

- Hosting a cloud review service.
- Operating a DiffVouch-owned global GitHub App or automatically reviewing every pull request.
- Automatically approving or requesting changes on GitHub.
- Replacing tests, linters, static-analysis tools, or human reviewers.
- Editing code or automatically applying suggestions.
- Reviewing an entire repository when no relevant diff exists.
- Combining Codex and Claude results in one review.
- Supporting non-GitHub code-hosting platforms.
- Reviewing binary file contents.
- Running untrusted code from the reviewed repository.

## 5. Core User Journeys

### Review Local Changes

```bash
diffvouch review --provider codex
```

This reviews staged changes, unstaged tracked-file changes, and untracked text files. The comparison base is `HEAD`.

### Review Work Against a Base Branch

```bash
diffvouch review --provider claude --base main
diffvouch review --provider codex --base release/2.x
```

DiffVouch finds the merge base between `HEAD` and the named branch, then reviews everything between that merge base and the current working tree. It does not implicitly fetch or modify Git state.

### Use a Provider API

```bash
diffvouch review --provider claude --transport api
```

CLI transport is the default. API usage must be explicitly selected so DiffVouch cannot unexpectedly incur API charges.

### Publish to GitHub

```bash
diffvouch review --provider codex --base main --publish
```

DiffVouch publishes an overall comment-only PR review containing the rating and rubric breakdown. Actionable findings are attached to relevant changed lines where GitHub permits it. Publishing never approves a PR or formally requests changes.

### Use DiffVouch as a Quality Gate

```bash
diffvouch review --provider codex --fail-below 3.5
diffvouch review --provider codex --fail-on-severity high
```

Without a configured threshold, a completed review exits successfully regardless of its rating.

## 6. CLI Interface

### Primary Command

```text
diffvouch review [options]
```

The provider is required and DiffVouch must never silently choose one:

```text
--provider <codex|claude>
```

Diff selection:

```text
--base <git-ref>          Review from merge-base(ref, HEAD) through the working tree
--committed-only          Exclude staged, unstaged, and untracked changes
--staged-only             Review only staged changes; incompatible with --base
--exclude <glob>          Add a one-run exclusion pattern; repeatable
```

Provider controls:

```text
--provider <codex|claude>
--transport <cli|api>     Default: cli
--model <model-id>        Optional provider-specific override
--effort <level>          Optional provider reasoning-effort override
```

Output and publishing:

```text
--format <terminal|json>  Default: terminal
--output <path>           Write the result to a file in addition to stdout
--publish                 Publish to the current branch's GitHub PR
--repo <owner/name>       Override GitHub repository detection
--pr <number>             Override automatic PR lookup
```

Quality gates:

```text
--fail-below <1..5>
--fail-on-severity <critical|high|medium|low>
```

General behavior:

```text
--config <path>           Override configuration path; repository-local paths resolve from the trusted commit
--verbose
--no-color
--version
--help
```

Incompatible argument combinations must fail before invoking an AI provider.

## 7. Repository Configuration

DiffVouch supports an optional committed `.diffvouch.yml` file:

```yaml
version: 1

provider:
  default_transport: cli
  models:
    codex: null
    claude: null

review:
  rubric:
    correctness: 35
    security: 20
    maintainability: 20
    testing: 15
    scope: 10
  instructions:
    - "Public API changes must remain backward compatible."
  exclude:
    - "vendor/**"
    - "dist/**"
    - "*.min.js"
    - "package-lock.json"
  max_diff_bytes: 500000

quality_gate:
  fail_below: null
  fail_on_severity: null

```

Configuration rules:

- Rubric weights must be non-negative and total 100.
- Unknown keys produce a configuration error.
- CLI options override repository configuration.
- A provider remains mandatory on the command line.
- Configuration cannot enable automatic publishing or contain credentials.
- API transport cannot be selected by repository configuration.
- Repository configuration is read from the trusted base commit. Changes to the
  configuration remain review data until they are merged.
- An explicit configuration outside the repository is read from the local
  filesystem and is therefore an intentional user-controlled trust source.
- Excluded files appear in the local summary with the reason they were skipped.

## 8. Diff Collection Requirements

### Local Mode

With no diff-selection flags, DiffVouch compares the working tree to `HEAD` and includes staged, unstaged, and untracked text-file changes. In a repository with no `HEAD`, every eligible text file is treated as newly added.

### Base-Branch Mode

With `--base <ref>`, DiffVouch must:

1. Validate that the ref resolves locally.
2. Find `git merge-base HEAD <ref>`.
3. Collect committed changes from the merge base to `HEAD`.
4. Overlay staged, unstaged, and eligible untracked changes.
5. Produce one effective patch representing the working tree relative to the merge base.

`--committed-only` stops at `HEAD` and excludes the working-tree overlay.

### File Handling

- Detect renames and deletions.
- Preserve old and new paths.
- Skip binary files while listing them in the summary.
- Respect `.gitignore` for untracked files.
- Apply DiffVouch exclusions after Git identifies candidates.
- Credential redaction is temporarily disabled because line-level replacement
  produced false findings. Warn users that reviewable patch text is sent unchanged.
- Never invoke repository hooks or execute repository code.
- Exit without contacting a provider when the resulting diff is empty.
- Report submodule pointer changes without recursively reviewing submodule contents.
- Never follow untracked symlinks outside the repository.

### Large Diffs

DiffVouch bounds Git output while reading it and fails closed when the configured
maximum diff size is exceeded. Complete patches within that maximum are split by
file and hunk at the provider chunk limit, reviewed independently, and combined
deterministically into one deduplicated result.

Any omitted content makes the review partial. Partial reviews must be clearly labeled and cannot be published in the MVP.

## 9. Provider Architecture

Both providers implement the same internal adapter:

```text
ProviderAdapter.review(request) -> ReviewResult
```

The request contains repository metadata, revision metadata, the effective patch,
changed-file inventory, rubric, repository instructions, and the required output
schema. Until syntax-preserving redaction is implemented, patch text is sent unchanged.

### Codex Adapter

- CLI transport invokes the locally installed and authenticated Codex CLI.
- API transport uses `OPENAI_API_KEY` or a key stored through `diffvouch auth set-key openai`.
- Missing CLI authentication produces setup guidance and never silently falls back to the API.

### Claude Adapter

- CLI transport invokes the locally installed and authenticated Claude Code CLI.
- API transport uses `ANTHROPIC_API_KEY` or a key stored through `diffvouch auth set-key anthropic`.
- Missing CLI authentication produces setup guidance and never silently falls back to the API.

### Provider Safety

Repository content, comments, filenames, and diff text are untrusted data. Instructions found in reviewed code must not override DiffVouch's review instructions.

Provider output must validate against the result schema. Invalid output fails
without a repair call, rating, or publication so DiffVouch cannot silently incur
additional API cost or claim an incomplete review.

## 10. Review Rubric and Rating

| Dimension | Weight | Meaning |
|---|---:|---|
| Correctness | 35% | Logic errors, broken behavior, edge cases, concurrency, and error handling |
| Security | 20% | Vulnerabilities, unsafe data handling, authorization, secrets, and injection risks |
| Maintainability | 20% | Clarity, unnecessary complexity, duplication, and architectural fit |
| Testing | 15% | Missing or inadequate tests for changed behavior and likely regressions |
| Scope | 10% | Unrelated changes, accidental files, compatibility, and change-set focus |

Each dimension receives a score from 1.0 to 5.0. The overall score is the weighted arithmetic mean, rounded to one decimal place and displayed as `x.x/5`.

Rating meanings:

- `4.5-5.0`: Excellent; no meaningful concerns found.
- `3.5-4.4`: Good; minor improvements recommended.
- `2.5-3.4`: Needs work; one or more material concerns.
- `1.5-2.4`: High risk; substantial fixes recommended.
- `1.0-1.4`: Critical risk; should not be merged in its current form.

Any critical finding caps the overall score at 1.5. Any high-severity finding
caps it at 2.5. DiffVouch enforces these caps after receiving provider output.

Repositories may replace weights and add instructions, but all five dimensions remain present so ratings stay comparable.

## 11. Finding Model

Every finding contains:

```text
id
severity: critical | high | medium | low
category: correctness | security | maintainability | testing | scope
title
explanation
recommendation
path
line
side: old | new
confidence: high | medium | low
evidence
```

Finding requirements:

- Describe a concrete risk introduced or exposed by the diff.
- Reference a changed line when published inline.
- Allow general design or missing-test findings to omit a line and appear in the summary.
- Distinguish observed defects from uncertainty.
- Keep low-confidence findings local or in a "Needs verification" summary section rather than publishing them inline.
- Merge duplicates produced by chunked analysis.
- Reference score-affecting findings in the rating explanation.

## 12. Local Output

Terminal output contains:

1. Review scope and resolved comparison.
2. Provider and transport.
3. Overall rating and qualitative label.
4. Rubric score table.
5. Findings grouped by severity.
6. File, line, explanation, and recommendation for every finding.
7. Positive observations.
8. Skipped, excluded, redacted, or partially reviewed files.
9. Quality-gate result.
10. A notice confirming that nothing was published unless `--publish` was supplied.

Color is used only when stdout is interactive and neither `NO_COLOR` nor `--no-color` disables it.

JSON output is stable, versioned, and contains all review data without terminal formatting.

## 13. Public Result Schema

```json
{
  "schemaVersion": 1,
  "reviewId": "uuid",
  "status": "complete",
  "partial": false,
  "scope": {
    "mode": "working-tree",
    "baseRef": "HEAD",
    "baseSha": "...",
    "mergeBase": null,
    "headSha": "..."
  },
  "provider": {
    "name": "codex",
    "transport": "cli",
    "model": "...",
    "effort": null
  },
  "rating": {
    "overall": 3.8,
    "label": "Good",
    "dimensions": {
      "correctness": 3.5,
      "security": 4.5,
      "maintainability": 4.0,
      "testing": 3.0,
      "scope": 4.5
    }
  },
  "findings": [],
  "positiveObservations": [],
  "needsVerification": [],
  "files": {
    "reviewed": [],
    "excluded": [],
    "binary": [],
    "omitted": [],
    "redactions": 0
  },
  "gate": {
    "passed": true,
    "reasons": []
  },
  "publication": {
    "requested": false,
    "published": false,
    "url": null,
    "bot": null
  }
}
```

Additive fields may be introduced within schema version 1. Renaming, removing, or changing field meanings requires a schema-version increment.

## 14. GitHub Integration

### Authentication and Discovery

DiffVouch publishes through a private GitHub App owned by the user or the
repository-owning organization. There is no DiffVouch-hosted bot or backend.
The app requires `Pull requests: Read and write`; `Contents` access and webhooks
are unnecessary because reviews use the local checkout and run only on demand.

DiffVouch must:

1. Create the private app through GitHub's App Manifest flow. The CLI starts a
   temporary loopback callback, submits a manifest with only pull-request write
   permission, no events, inactive webhooks, and no OAuth-on-install request,
   verifies an unguessable state value, and exchanges the one-time code within
   the allowed lifetime. The user only confirms the app name and ownership on
   GitHub; they do not manually configure permissions or download a PEM.
2. Offer `--no-browser` for headless use and `--code` as a copy/paste fallback
   when the localhost redirect cannot complete. Keep manual configuration for
   existing apps, not as the primary creation path.
3. Store the App ID, slug, hostname, API/web base URLs, and generated private key locally.
   Prefer the OS credential manager and allow a mode-0600 user file only as fallback.
4. Sign a short-lived RS256 JWT with an issued-at adjustment for clock drift.
5. Locate the repository installation with `GET /repos/{owner}/{repo}/installation`.
6. Exchange the JWT for an installation token restricted to the current repository
   and `pull_requests: write`.
7. Keep installation tokens only in memory and never log credentials or tokens.
8. Read the current Git remote and resolve hostname, owner, and repository.
9. Find exactly one open PR matching the local branch and head SHA, including fork PRs,
   or honor explicit `--repo` and `--pr` overrides.
10. Revalidate the reviewed base and head SHAs immediately before publication.

Private apps can only be installed on the account that owns them, so organization
repositories normally require an app created under that organization. GitHub
Enterprise Server is supported through configurable web and REST API base URLs.

Failure to resolve exactly one PR stops publishing and prints corrective guidance. The completed local review remains available.

### Publication Behavior

- Publication requires `--publish`.
- The GitHub review uses the `COMMENT` event.
- High-confidence findings on eligible changed lines become inline comments.
- Medium-confidence findings may be published inline but must be labeled.
- Low-confidence findings appear in the summary under "Needs verification."
- Findings on non-commentable lines move to the summary.
- The summary includes the score, rubric breakdown, finding count, provider, and reviewed commit.
- Each invocation creates a new review associated with the reviewed commit.
- DiffVouch confirms that the PR base and head SHAs still match the reviewed SHAs immediately before publishing.
- A mismatched base or head SHA aborts publication and requests a fresh review.
- Reviews are attributed to `<app-slug>[bot]`, not the invoking human.
- Partial publication failures report exactly what was posted.

## 15. Exit Codes

```text
0  Review completed and configured quality gates passed
1  Review completed but a configured quality gate failed
2  Invalid command arguments or configuration
3  Git or diff collection failure
4  Provider unavailable, unauthenticated, or returned invalid output
5  GitHub discovery, authentication, or publication failure
6  Review could not safely cover the requested diff
```

A GitHub publication failure uses exit code 5 even if the local review succeeded, because `--publish` made publication part of the requested operation.

## 16. Feature List

### MVP: Must Have

- Local CLI application.
- Explicit Codex or Claude provider selection.
- Authenticated Codex CLI integration.
- Authenticated Claude Code CLI integration.
- Explicit OpenAI and Anthropic API transports.
- Review of staged, unstaged, and untracked files.
- Merge-base comparison against any local Git ref.
- Optional committed-only and staged-only modes.
- Binary-file detection and ignored-file handling.
- Large-diff chunking and partial-review protection.
- Default weighted five-dimension rubric.
- Repository-level rubric and exclusion overrides through `.diffvouch.yml`.
- Structured findings with severity, category, file, and line.
- Transparent overall score out of 5.
- Human-readable terminal output.
- Versioned JSON output.
- Optional score and severity quality gates.
- User- or organization-owned private GitHub App authentication and PR discovery.
- Explicit GitHub publication.
- PR summary plus eligible inline comments.
- GitHub `COMMENT` review state only.
- Commit-SHA validation before publication.
- Prompt-injection defenses and an explicit warning that redaction is disabled.
- Clear exit codes and actionable errors.

### Shortly After MVP

- Markdown output.
- Review only selected paths.
- Compare an explicit commit range.
- Global user configuration.
- Custom prompt files.
- Suppression of findings by ID.
- Review history stored locally.
- Cost, token, and elapsed-time estimates.
- Optional inclusion of relevant nearby source files.
- Shell completion.
- Machine-readable progress events.
- GitHub Actions integration using API credentials.

### Future Possibilities

- Dual-provider consensus reviews.
- GitLab and Bitbucket publishing.
- GitHub Check Runs.
- Team policy bundles.
- Changed-code test execution.
- Suggested patch generation and safe application.
- Baseline tracking so only new findings fail a gate.
- IDE extension.
- Hosted team dashboards and review analytics.

## 17. Errors and Edge Cases

- Outside a Git repository: fail before contacting a provider.
- Empty diff: exit successfully with "No reviewable changes."
- Missing provider flag: show supported values and fail argument validation.
- Missing provider CLI: show installation and login guidance.
- Missing API key: identify the required environment variable.
- Invalid base ref: report it without fetching.
- No merge base: explain that the histories are unrelated.
- Provider timeout or interruption: publish nothing.
- Invalid model response: fail safely without assigning a rating or publishing.
- Detached `HEAD`: local review works; PR lookup requires `--pr`.
- Dirty tree with `--committed-only`: ignore local modifications and state that they were excluded.
- Changed PR head: abort publishing.
- Non-commentable GitHub line: move the finding into the summary.

## 18. Testing and Acceptance Criteria

### Diff Collection

- Default mode includes staged, unstaged, and untracked text changes.
- Ignored and binary untracked files are excluded and reported.
- `--staged-only` excludes unstaged and untracked files.
- `--base main` uses the merge base, not the current `main` tip, as its starting point.
- `--base main --committed-only` excludes working-tree changes.
- Rename, deletion, unborn branch, detached HEAD, submodule, symlink, and unrelated-history cases behave as specified.

### Providers

- Each adapter invokes only the selected provider.
- CLI mode never silently switches to a billable API.
- API mode requires the correct environment variable.
- Invalid structured output fails closed without a rating or publication.
- Repository content cannot override review instructions.
- Timeout and cancellation produce no GitHub side effects.

### Ratings

- Default weights total 100.
- Weighted scores are deterministic and rounded to one decimal.
- Correctness and security severity caps are enforced.
- Invalid custom weights fail before provider invocation.
- Score explanations reference material findings.

### Output and Quality Gates

- Terminal output includes scope, provider, score, dimensions, findings, exclusions, and gate status.
- JSON output validates against schema version 1.
- `--no-color` and non-interactive stdout contain no ANSI escapes.
- Output distinguishes complete and partial reviews.
- Without a threshold, a low rating still exits with code 0.
- Score and severity thresholds produce exit code 1 when breached.

### GitHub

- Nothing is published without `--publish`.
- Publishing resolves the correct repository and open PR.
- The review uses the `COMMENT` event.
- Eligible findings attach to changed lines.
- Non-commentable and low-confidence findings appear in the summary.
- A changed PR head SHA stops publication.
- Partial reviews cannot be published.
- Publication failures identify any successfully created GitHub content.

### MVP Acceptance

The MVP is ready when a developer can:

1. Install DiffVouch on a supported desktop platform.
2. Review all local changes through either authenticated provider CLI.
3. Review committed and local branch changes relative to a named base.
4. Receive a validated rating, rubric breakdown, and line-level findings.
5. Override the rubric through a committed configuration file.
6. Use the result as an optional local quality gate.
7. Explicitly publish the same review to the correct GitHub PR.
8. Trust that DiffVouch will not publish, incur API charges, fetch Git state, or execute repository code without an explicit request.

## 19. Success Measures

For an initial private beta:

- At least 90% of reviews return valid structured output on the first provider call.
- At least 80% of published inline findings resolve to valid GitHub diff positions.
- Fewer than 10% of findings are marked unhelpful by users.
- Median setup time is under five minutes for a developer already authenticated with a provider CLI and a configured GitHub App.
- No review is published without an explicit `--publish`.
- No API transport is used without explicit `--transport api`.
- The same structured provider response always produces the same rating and gate result.

## 20. Delivery Phases

### Phase 1: Review Engine

Implement Git inspection, effective-patch construction, configuration validation, structured result types, rating calculation, terminal output, and JSON output.

### Phase 2: Provider Adapters

Add Codex CLI, Claude Code CLI, OpenAI API, and Anthropic API transports with
strict output validation, timeouts, and cancellation.

### Phase 3: GitHub Publication

Add manifest-based private GitHub App creation, manual existing-app configuration,
secure key storage, RS256 and installation-token authentication, repository and PR
discovery, line mapping, summary generation, inline comments, and base/head SHA
safety checks.

### Phase 4: Hardening and Release

Complete cross-platform tests, large-diff handling, syntax-preserving redaction,
prompt-injection defenses, packaging, installation documentation, and strictly
local or opt-in telemetry.

## 21. Assumptions and Defaults

- DiffVouch is a CLI-first, local-first product.
- The MVP targets individual developers.
- Codex and Claude are both MVP providers.
- Users explicitly select a provider on every run.
- Provider CLI transport is the default; API transport is explicit.
- DiffVouch does not operate a backend or store source code.
- All local changes are reviewed by default.
- Base-branch reviews compare the merge base to the current working tree.
- GitHub publication is explicit and uses a comment-only review.
- GitHub reviews use a private app owned by the user or repository organization.
- Ratings use a built-in weighted rubric with optional repository overrides.
- Reviews are informational unless a quality threshold is configured.
- The initial configuration format is `.diffvouch.yml`.
- The CLI is implemented in Go and distributed as standalone native executables
  for macOS, Linux, and Windows on AMD64 and ARM64. End users do not need a Go runtime.

## 22. Post-MVP Roadmap

These capabilities are intentionally deferred from the current CLI pull request.
Each should have a dedicated GitHub issue before implementation, with this section
as the durable product-level source of requirements.

### Event-Driven Pull Request Reviews

- Allow repositories to opt in to automatic DiffVouch reviews when a pull request
  is opened, reopened, or receives new commits.
- Allow a user to request a review by mentioning the DiffVouch bot in a pull-request
  conversation.
- Publish the result using the configured DiffVouch GitHub App identity.
- Avoid duplicate reviews for the same pull-request revision.
- Preserve DiffVouch's security and privacy guarantees when reviews are triggered
  automatically.

### False-Positive Disposition and Suppression

- Allow an authorized user to mark a review finding as a false positive and provide
  an optional reason.
- Prevent the same false-positive finding from being reported again in later
  reviews within the selected suppression scope.
- Allow users to view and remove recorded suppressions.
- Make suppressed findings visible as a count or summary so reviews remain
  transparent and auditable.
- Avoid suppressing materially different findings by mistake.
