# Test repository access during PR reviews

Use the same PR revision, provider, model, and effort for the old and new builds.
Choose a PR with a known bug that depends on unchanged code, such as a changed
return value that breaks an existing caller or an existing test. A larger number
of findings alone does not demonstrate a better review.

## Build both versions

From the DiffVouch checkout on this feature branch, with Go installed:

```bash
git fetch origin main
review_test_dir="$(mktemp -d)"
git worktree add --detach "$review_test_dir/baseline" origin/main
git worktree add --detach "$review_test_dir/candidate" HEAD
(cd "$review_test_dir/baseline" && go build -o "$review_test_dir/diffvouch-before" ./cmd/diffvouch)
(cd "$review_test_dir/candidate" && go build -o "$review_test_dir/diffvouch-after" ./cmd/diffvouch)
```

Run this comparison before the feature is merged, so `origin/main` is still the
patch-only baseline. Keep the same terminal open for the following commands.

## Verify the tool integration without API charges

```bash
(cd "$review_test_dir/candidate" && go test -race ./internal/provider -run 'TestAPIReviewExecutesToolsWithoutProviderCLIs|TestBash|TestReadFile' -v)
```

The API integration test removes both provider CLIs from PATH, supplies simulated
API replies, and executes real file reads and Bash commands. It verifies multiple
tool rounds, context outside the repository, stdout/stderr, and nonzero exit
status for both providers. This checks tool execution, not live model quality.

## Compare live reviews of the same PR

Install Bash on PATH. Configure the API key for the provider you want to test:

```bash
"$review_test_dir/diffvouch-after" auth set-key openai
# Or:
"$review_test_dir/diffvouch-after" auth set-key anthropic
```

These live reviews make billable API requests. Neither Codex CLI nor Claude Code
is needed. Use a fresh checkout of the target repository so local edits do not
affect file inspection or test results. Replace `OWNER/REPO`, `123`, and the model
placeholder below:

```bash
gh repo clone OWNER/REPO "$review_test_dir/target"
cd "$review_test_dir/target"
review_pr=123
gh pr checkout "$review_pr"
review_base_sha="$(gh pr view "$review_pr" --json baseRefOid --jq .baseRefOid)"
git fetch origin "$review_base_sha"

review_provider=codex
review_model=YOUR_OPENAI_MODEL
# For Anthropic, use review_provider=claude and an Anthropic model.

"$review_test_dir/diffvouch-before" review \
  --provider "$review_provider" --transport api --model "$review_model" \
  --base "$review_base_sha" --committed-only \
  --format json --output "$review_test_dir/before.json"

"$review_test_dir/diffvouch-after" review \
  --provider "$review_provider" --transport api --model "$review_model" \
  --base "$review_base_sha" --committed-only \
  --format json --output "$review_test_dir/after.json"
```

The `--base` and `--committed-only` combination reviews the PR's committed diff
without requiring a DiffVouch GitHub App. If that app is already configured for
the target repository, you can replace those two options with `--pr "$review_pr"`.
Neither command publishes a GitHub comment unless you add `--publish`.

Compare the JSON reports for:

- Whether the known bug was found and the explanation agrees with the unchanged
  caller, dependency, or test that makes it a bug.
- Whether suspected problems were dismissed correctly after inspecting context.
- Specific commands and test results reported in the summary, and incomplete
  checks recorded in `needsVerification`. Check these against your own test run.
- Findings attached to changed lines, with concrete evidence and no invented
  test results.

Repeat the comparison three times with separate output filenames. Review quality
varies between runs; count correct findings and false positives against the known
issues rather than comparing the overall numeric rating. Keep the PR revision
fixed, and confirm `git status --short` shows no source edits after each review.

The reviewer chooses which tools to call. A successful review alone does not
prove it ran commands, and the final report is not an execution log. The local
integration test above verifies the command execution path deterministically;
the live comparison evaluates whether that capability improves findings.
