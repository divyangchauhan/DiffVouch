# AI code-review prompt research

Research date: 2026-09-05

## Executive summary

The public evidence points to a review system, not merely a longer prompt. The
best systems narrow the review target to regressions introduced by the change,
retrieve only relevant context, generate candidate findings through focused
lenses, verify or attempt to refute those candidates, deduplicate them, and then
publish a small number of severity-calibrated comments. Their customization
models separate durable defaults, repository or path-specific rules, one-run
instructions, and explicit exclusions.

DiffVouch's CLI intentionally has a more restrictive operating model: the
provider sees a sanitized patch and has no tools or repository access. Within
that constraint, the prompt should optimize for precision, clearly describe the
incomplete-diff limitation, ask for an internal candidate-verification pass,
and refuse work beyond the patch's scope. Context retrieval and multi-agent
verification are valuable future improvements, but should not be simulated or
claimed by the current prompt.

## Public systems and prompts

| System | Publicly visible approach | Useful lesson for DiffVouch |
|---|---|---|
| [Qodo PR-Agent reviewer prompt](https://github.com/qodo-ai/pr-agent/blob/main/pr_agent/settings/pr_reviewer_prompts.toml) | Its exact prompt is public. It limits review to new code and issues introduced by the PR, warns that hunks are incomplete context, requires a concrete scenario, prefers silence over lower-confidence speculation, and accepts an empty finding list. Its configuration also caps the default review at three key findings and supports extra instructions. | State the causal scope rule literally; make concrete trigger, impact, and confidence admission criteria; explicitly avoid conclusions from omitted context. |
| [AI Diff Reviewer default prompt](https://github.com/DailybotHQ/ai-diff-reviewer/blob/main/prompts/default.md) and [customization design](https://github.com/DailybotHQ/ai-diff-reviewer/blob/main/docs/PROMPTS.md) | The public prompt asks whether a concrete failure mode exists, requires diff-line anchors, separates severity levels, filters lint/style/speculation, and favors high signal over volume. The tool supports a default, an appended extension, or a full replacement. | Keep invariant review mechanics separate from customizable policy; define severity operationally; require changed-line evidence. |
| [pr-review agents](https://github.com/llimllib/pr-review/blob/main/src/agents.ts) | Public prompts divide work into bugs, tests, impact, and quality, give agents read-only context tools, repeat the changed-code scope in every lens, and use a final summarizer to filter out-of-scope and duplicate findings. | Repeating the scope constraint near each decision is useful. Specialized lenses and synthesis are promising when DiffVouch later supports multi-pass review. |
| [Sentry code-review skill](https://github.com/getsentry/skills/blob/main/skills/code-review/SKILL.md) | The checklist covers runtime failures, performance, side effects, compatibility, security, tests, and long-term risk. Its approval rule says not to block for style and frames the goal as risk reduction rather than perfect code. | Keep the default checklist risk-oriented and make style non-blocking or omit it. |
| [GitHub Copilot code review](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/request-a-code-review/use-code-review) and [sample review prompt](https://docs.github.com/en/copilot/tutorials/customization-library/prompt-files/review-code) | Copilot attaches severities and suggestions, and accepts repository-wide and path-specific instructions. The sample prompt uses a review checklist and asks for line references, explanation, solution, and rationale. | Preserve a structured schema and support runtime/repository customization. DiffVouch should retain its safer base-revision policy rather than trust instructions changed by the patch itself. |
| [CodeRabbit path instructions](https://docs.coderabbit.ai/configuration/path-instructions), [code guidelines](https://docs.coderabbit.ai/knowledge-base/code-guidelines), and [learnings](https://docs.coderabbit.ai/knowledge-base/learnings) | An internal base prompt was not located in the public material, but its control plane is documented: filter irrelevant files, apply guidance only to matching paths, ingest established standards, and learn scoped preferences from feedback. CodeRabbit recommends using path instructions as targeted supplements and explaining why a learned preference exists. | Add path-scoped rules later; keep custom instructions focused and rationale-bearing; record accepted/rejected feedback rather than endlessly expanding the base prompt. |
| [Claude Code Review](https://support.claude.com/en/articles/14233555-set-up-code-review-for-claude-code) | Multiple specialist agents inspect the diff with repository context; a distinct verification step checks candidates against actual behavior; results are deduplicated and ranked. The default emphasizes production correctness rather than formatting or blanket test-coverage comments. `CLAUDE.md` and `REVIEW.md` add scoped rules. | Candidate refutation, deduplication, and correctness-first defaults are high-value. A future second model call should be a skeptical verifier rather than another unconstrained reviewer. |
| [Graphite Agent customization](https://graphite.com/docs/ai-review-customization) | Custom prompts, file-based rules, exclusions, and per-rule acceptance metrics are separate concepts. Graphite recommends one concern per rule, specific bad/good examples and rationale, then testing the rule against recent PRs. | Do not turn exclusions into vague negative rules. Evaluate each prompt/rule on historical diffs and measure whether engineers act on its findings. |
| [Greptile custom context](https://www.greptile.com/docs/code-review-bot/custom-context) | Rules, style guides, and other context can be scoped to repositories and file patterns, alongside codebase indexing. | Relevant context beats globally injecting every available guideline. |
| [Bito agent customization](https://docs.bito.ai/ai-code-reviews-in-git/install-run-using-bito-cloud/create-or-customize-an-agent-instance) | It separates essential versus comprehensive feedback modes, incremental review, custom guidelines, filters, static-analysis tools, and adaptive suppression of repeatedly ignored non-critical suggestions. | Strictness and scope are distinct controls; deterministic tools should handle deterministic checks; feedback can tune noise without suppressing critical issues. |

Human review guidance leads to the same shape. Google's
[review checklist](https://google.github.io/eng-practices/review/reviewer/looking-for.html)
covers design, functionality, complexity, tests, naming, comments, style, and
documentation, while its [comment-writing guidance](https://google.github.io/eng-practices/review/reviewer/comments.html)
asks reviewers to explain why, label severity, distinguish optional comments,
and comment on the code rather than the developer.

### Reuse and licensing

The prompt-bearing [Qodo PR-Agent](https://github.com/qodo-ai/pr-agent/blob/main/LICENSE),
[AI Diff Reviewer](https://github.com/DailybotHQ/ai-diff-reviewer/blob/main/LICENSE),
and [pr-review](https://github.com/llimllib/pr-review/blob/main/LICENSE)
repositories currently publish under the MIT license; [Sentry's skills
repository](https://github.com/getsentry/skills/blob/main/LICENSE) uses Apache
2.0. Direct copying or redistribution must follow the relevant notice and
license terms. The DiffVouch prompt is an original synthesis of the recurring
review principles above, not a verbatim copy. Product documentation describes
useful behavior but should not be treated as an open-source prompt license.

## Resulting DiffVouch prompt design

The implemented prompt has four layers:

1. An invariant authority, safety, scope, evidence, severity, location, scoring,
   and structured-output contract.
2. Default review guidance from `internal/review/prompts/default.md`, or an
   explicit runtime replacement.
3. repository review instructions loaded from the trusted comparison revision.
4. The sanitized patch in a lower-authority user-data envelope.

Runtime guidance wins if it conflicts with a repository instruction because it
is an explicit command-line choice by the invoking user. Both remain subordinate
to the invariant contract.

The invariant layer deliberately keeps these rules even under a custom prompt:

- Only report behavior introduced, exposed, or materially worsened by the patch.
- Do not request unrelated features, broad refactors, generic hardening, or
  blanket tests/documentation.
- Do not infer repository-wide absence from an incomplete diff or from one chunk.
- Require a concrete trigger, changed behavior, observable impact, and smallest
  credible fix.
- Challenge candidate findings and route material uncertainty to
  `needsVerification`.
- Anchor inline findings only to changed lines and calibrate severity against
  operational impact and likelihood.
- Treat empty findings as a successful result.

The default guidance adds a compact risk checklist and noise filters. It is kept
in Markdown rather than a large Go string so maintainers can review prompt-only
changes and evaluate them independently.

## Runtime customization contract

`--prompt <text>` and `--prompt-file <path|->` replace the default guidance for
one invocation. They are mutually exclusive, reject empty input, and limit input
to 256 KiB. `--prompt-file -` reads stdin. A runtime override does not remove the
invariant safety/scope/schema wrapper or trusted repository instructions.

This design makes quick experiments possible without making successful
structured output depend on every custom author reproducing DiffVouch's JSON
contract. Permanent repository rules should continue to use
`.diffvouch.yml` `review.instructions`; those rules are loaded from the trusted
base revision so a PR cannot weaken its own review.

## Evaluation plan

Prompt changes should be regression-tested rather than accepted because they
sound stricter. Recent work such as [SWE-PRBench](https://arxiv.org/abs/2603.26130),
[SWRBench](https://arxiv.org/abs/2509.01494), and
[ContextCRBench](https://arxiv.org/abs/2511.07017) uses real pull requests and
human-verified/context-enriched review targets. [RepoAudit](https://proceedings.mlr.press/v267/guo25n.html)
also supports the value of a validator that checks candidate bugs and path
conditions to reduce false positives.

For DiffVouch, build a versioned local eval set containing both clean changes and
bug-introducing changes from the project's own history. Each case should label
the introduced defect, acceptable changed-line anchors, severity band, relevant
dimension, and tempting-but-out-of-scope observations. Include adversarial cases
for prompt injection, incomplete hunks, guards outside the hunk, tests in another
chunk, deleted-line regressions, generated files, pure refactors, and clean diffs.

Track at least:

- finding precision and defect recall;
- clean-change false-positive rate;
- out-of-scope, duplicate, and invalid-location rates;
- first-pass schema validity;
- severity and blocking agreement;
- useful `needsVerification` rate versus disguised speculation;
- latency and token cost by provider/model.

Use blind A/B runs over multiple seeds or repeated trials where supported. Make
precision and out-of-scope regressions release blockers, while setting an
explicit acceptable recall floor. Review accepted/dismissed findings periodically
and turn recurring, well-explained patterns into focused repository rules rather
than continually lengthening the global prompt.
