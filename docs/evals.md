# DiffVouch evaluation design

This document records the evaluation design interview. The scope below is settled. [Running evaluations](eval-running.md) documents the implementation.

## Goals

- Detect review regressions caused by changes to DiffVouch code or prompts.
- Measure the quality of DiffVouch's pull request reviews.
- Compare DiffVouch with other review tools using benchmarks.

## Evaluation target

The primary target is the Go CLI's native reviewer. The intended evaluation path does not invoke the Codex or Claude Code harnesses or the standalone DiffVouch skill.

Those integrations might be removed in the future. This evaluation work does not authorize removing them. The user has explicitly requested implementation of ChatGPT subscription access within the native Go CLI as a prerequisite for running evaluations.

## Quality dimensions

Measure actionable correctness and security findings through both bug detection and finding validity. Separately evaluate code quality, simplicity, and cleanliness. Count maintainability findings only when they offer concrete benefits, such as reducing unnecessary complexity or duplication, clarifying confusing logic, or following established repository conventions. Pure taste and formatting preferences earn no credit. Sol grades findings as valid, invalid, or unresolved; Astra adjudicates unresolved and valid unmatched findings.

## Evaluation collections and comparisons

Use Martian's complete 50-PR offline suite and all 350 SWE-PRBench PRs, retaining the published 100-PR split as a separate report. Include all eligible PRs from the user's public repositories rather than limiting evaluation to 30. The user authorizes additional useful public benchmarks. Keep suite results separate and report actual unique PR and repository coverage. A partial run must report its case coverage and cannot be compared directly with a full-suite aggregate.

Include PRs from the user's public repositories. The current origin identifies `divyangchauhan/DiffVouch`; the initial public inventory will use that owner. Treat all personal PRs as unlabeled until assessed, including merged PRs. Ownership and merge status do not imply correctness or code quality.

The public inventory includes [DiffVouch](https://github.com/divyangchauhan/DiffVouch), [Pramana](https://github.com/divyangchauhan/Pramana), [Shruti](https://github.com/divyangchauhan/Shruti), [Tarpan](https://github.com/divyangchauhan/Tarpan), [Mushak](https://github.com/divyangchauhan/Mushak), [jobsieve](https://github.com/divyangchauhan/jobsieve), [divyang.dev](https://github.com/divyangchauhan/divyang.dev), and [PixResize](https://github.com/divyangchauhan/PixResize). These provide Go, Python, C#, TypeScript, Rust, and JavaScript PRs. Pin immutable revisions when assembling cases; repository and PR inventories can change.

Competitor comparisons use published benchmark results only. The user does not want to pay to run competing tools. Preserve each published result's source, date, dataset, and scoring conditions; do not present historical scores as measurements of the current competing product.

The user approved regrading Martian's archived competitor review outputs alongside DiffVouch with the same chosen judge. Retain original published scores separately. Consistent regrading must include candidate extraction and deduplication, which are themselves model-dependent, as well as final matching. This produces a new comparison against historical competitor reviews without invoking competitor tools.

Selected suites and related sources:

- [Martian Code Review Bench](https://github.com/withmartian/code-review-benchmark/tree/e616e849755441da38f18bf3adba2c9583b03803): its offline suite contains 50 PRs from five projects, 173 expected findings, published competitor outputs, and scoring code. Use the pinned snapshot as the initial basis for reviewer-product comparisons. Its core scoring profile selects 158 expected findings; preserve benchmark scoring separately from DiffVouch's correctness/security and maintainability measures.
- [Greptile's published benchmark](https://www.greptile.com/benchmarks): historical catch rates on 50 cases, without false-positive scoring. Martian's offline suite is related to this corpus; treat overlap as shared evidence rather than independent coverage.
- [SWE-PRBench](https://github.com/FoundryHQ-AI/swe-prbench): a 350-PR dataset with a published 100-PR evaluation split. Its model baselines use frozen context, so native repository-access runs would have different evaluation conditions and would not establish a directly comparable product ranking.

## Resource constraint

Use the user's ChatGPT subscription for evaluations. The latest instruction authorizes using available subscription allowance until it is exhausted and supersedes the earlier 50% reserve. Stop evaluation model calls when the allowance is exhausted, preserve completed work, and never fall back to billable API calls. A quota stop is an incomplete run, not evidence of a review-quality failure.

The user wants to use an earned reset that expires on September 21. Read-only account inspection confirmed that this is distinct from the normal weekly reset. Read current quota and reset eligibility before applying it: an available reset is not necessarily applicable yet. On exhaustion, stop model calls and preserve the run; use the expiring reset when applicable before resuming. This is not authorization to buy additional credits or incur API charges.

Use `gpt-5.6-sol` with high reasoning for measured reviews and separate Sol judge sessions. Prefer `gpt-5.6-luna` for routine preparation and delegated work. Use Astra for difficult adjudications where stronger reasoning is useful, with the same escalation rule for DiffVouch and archived competitors. Record models and effort explicitly; changing the reviewer creates a separate baseline rather than silently altering a running comparison.

The newly added `subscription` transport uses native device authentication and the existing Go review loop. It keeps a separate DiffVouch session and never falls back to billable API calls. [Subscription transport notes](subscription-transport.md) record its protocol basis and validation limits.

[OpenAI's authentication documentation](https://developers.openai.com/codex/auth) distinguishes ChatGPT subscription access from API-key access and states that API-key usage is billed through the Platform account rather than included ChatGPT plan credits. Native subscription login and one live smoke review succeeded on 2026-09-19. The smoke review found an intentionally introduced off-by-one defect; it does not establish benchmark performance or implement the evaluation budget policy.

## Accepted first version

- Use a fixed Sol reviewer configuration for the first measured baseline, Luna for routine preparation, and a separate judge session with tool identities hidden. Model and effort changes create separate baselines.
- On the user's PRs, assess finding validity and concrete maintainability benefits. Do not report exhaustive bug recall until independently verified reference findings exist. Preserve unresolved judgments instead of forcing them into true or false positives.
- Keep each benchmark's prescribed scores separate from an additional evidence-based assessment of valid findings missing from its answer key.
- Start with resumable local runs and reports. Do not enforce regression thresholds or a blocking CI gate; the user will add enforcement after being satisfied with review quality. Record case revisions, code and prompt hashes, models, tool settings, outcomes, and failures. Repeat a fixed representative subset to estimate model variation.
- Reserve complete public benchmark suites for final comparison. Keep prompt-tuning examples separate from the frozen regression cases, with selection recorded before reading the evaluation results.

## Confidence and limits

Report per-suite and per-repository results, unresolved judgments, skipped cases, and execution failures alongside aggregate scores. Compare tools only on shared completed cases, while separately showing their coverage of the full suite. Preserve official benchmark scoring and additional finding-validity assessments as separate measures.

Use paired resampling of PRs to show uncertainty in score differences. Judge disagreement and model-run variation are separate sources of uncertainty and must be reported separately. Additional personal PRs improve DiffVouch coverage but do not expand the historical competitor comparison unless matching archived competitor outputs exist.

Select a small deterministic personal-PR development set before inspecting review outputs; all remaining eligible personal PRs are frozen evaluation cases. Large benchmark corpora remain evaluation-only. Do not describe a finite public corpus or an automated judge as proof that every bug has been found.

## Repository observations

The Go tests use mocked provider responses or fake provider executables to verify orchestration and output handling. The evaluation runner adds frozen public corpora, subscription-only live reviews, independent judging, and resumable reports.

The native review process can inspect repository files and execute commands. Whether a benchmark supplies sufficient repository context therefore matters when choosing cases and interpreting results.
