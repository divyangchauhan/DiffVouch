# Review quality improvements beyond prompt changes

Research date: 2026-09-21. These are proposed changes; this note does not implement them or resume evaluations.

## Recommendation

Build a staged review pipeline around the existing model call. The first priority is deterministic context and candidate coverage. The second is evidence-based verification and PR-level synthesis. Static analysis, tests, semantic deduplication, and adaptive compute should feed those stages. Explicit orchestration can enforce context selection and review coverage that the current model-directed tool use does not guarantee.

The ten-PR diagnostic supports this priority but does not prove it. Repository tools raised reference F2 from 41.4% to 47.2%, while the paired 95% interval was [-5.6, +15.2] points. The tool-enabled version still missed 17 of 32 benchmark issues, while the model judge marked only one of its 32 findings invalid. Unmatched claims are benchmark false positives even when the judge finds a real additional issue. These counts make coverage the first hypothesis to test. The small sample and model judge do not establish the cause, and they do not support blanket filtering. No version produced a valid maintainability finding. See [the paired report](eval-main-vs-tools-10.md#main-versus-repository-access-ten-prs).

Research gives useful design precedents, not a guaranteed score increase:

- AACR-Bench is directly about automated code review. Its authors report that context granularity and retrieval choice materially affect results, with effects varying by model, language, and agent setup. It is a 2026 preprint, so treat it as relevant evidence to test rather than a settled result. [Paper and artifacts](https://arxiv.org/abs/2601.19494)
- RepoCoder's iterative repository retrieval improved its in-file code-completion baseline by more than 10% across its reported settings. Code completion is not code review, but the controlled result supports testing targeted repository retrieval rather than relying on a model to discover every dependency through tools. [EMNLP 2023 paper](https://aclanthology.org/2023.emnlp-main.151/)
- Long-context models can perform worse when relevant evidence appears in the middle of a large input. That result comes from question answering and key-value retrieval, not source review. It argues for ranked, bounded context with explicit provenance instead of simply enlarging chunks. [TACL 2024 paper](https://aclanthology.org/2024.tacl-1.9/)
- Agentless separated localization, generation, and validation for program repair and reported competitive SWE-bench Lite results at low cost. Repair differs from review, but the study supports evaluating an explicit candidate and verification workflow instead of one unconstrained pass. [FSE 2025 paper](https://lingming.cs.illinois.edu/publications/fse2025.pdf)

First-party product accounts report similar structures, but they are not controlled comparisons and their quality claims should not be treated as proof. Cursor describes an early eight-pass, majority-vote, validator pipeline, then says its larger gains came from dynamic context and tool use. Anthropic describes parallel detection, verification, severity ranking, and compute that scales with PR complexity. These accounts justify testing those components separately. They do not justify copying a fixed number of passes or filtering every single-pass candidate. [Cursor Bugbot account](https://cursor.com/blog/building-bugbot), [Anthropic Code Review account](https://claude.com/blog/code-review)

## What the current code does

- `gitdiff.ChunkPatch` fills byte-limited chunks with whole file sections, then individual hunks when a file is too large. It has no dependency or symbol boundary model. [internal/gitdiff/gitdiff.go lines 383-424](../internal/gitdiff/gitdiff.go#L383-L424)
- `review.Perform` starts a fresh review for each chunk and concatenates the results. There is no final model pass that reasons across chunk findings or recovers a dependency split between chunks. [internal/review/review.go lines 111-123](../internal/review/review.go#L111-L123)
- The model decides which repository files and commands to inspect. The tool loop permits 32 rounds and 128 calls, while each tool result is capped at 64 KiB. This is a useful escape hatch, but it does not guarantee context coverage. [internal/provider/api.go lines 19-22](../internal/provider/api.go#L19-L22), [internal/provider/tools.go lines 17-48](../internal/provider/tools.go#L17-L48)
- Cross-chunk deduplication uses only `path:line:lowercase(title)`. Two descriptions of the same root cause at different lines remain separate, while title variation defeats the key. [internal/review/review.go lines 234-259](../internal/review/review.go#L234-L259)
- Location validation removes findings whose cited location is outside the reviewed path or changed lines and turns them into text in `needsVerification`. This checks publication coordinates, not whether the issue itself is valid. [internal/review/review.go lines 128-130](../internal/review/review.go#L128-L130), [internal/review/review.go lines 170-192](../internal/review/review.go#L170-L192)
- Evaluation records keep one status, error string, elapsed time, review, and judgments. They do not retain stage coverage, retrieved context, tool activity, candidate dispositions, or per-stage costs. [internal/eval/types.go lines 56-78](../internal/eval/types.go#L56-L78)

The paused full run `f003bd8b42da24a9` also shows why reliability needs its own workstream. Its local native records contain 81 completed cases and 30 errors: 19 checkout/dataset-patch mismatches, 9 incomplete subscription responses, 1 merge-base reconstruction failure, and 1 stream-read failure. Fixture preparation errors and provider transport errors should not be counted as review misses or mixed into an architectural quality conclusion.

## Ranked implementation plan

| Rank | Change | Expected effect | Main risk and control |
|---:|---|---|---|
| 1 | Deterministic change map and context packs | Raise coverage of cross-file behavior and make every chunk's inspected evidence measurable | Too much context. Rank snippets, enforce per-unit budgets, and record omissions. |
| 2 | Candidate discovery followed by targeted verification | Separate broad recall from the proof required for publication | Extra calls can exhaust the subscription. Verify only candidates that survive deterministic checks and cap work per PR. |
| 3 | Semantic chunking and PR-level synthesis | Recover issues that span files or hunks and merge evidence across chunks | Synthesis can invent new claims. Allow it to merge, rank, or request verification, but require new claims to enter the verifier. |
| 4 | Static-analysis and targeted-test evidence | Add deterministic candidates and confirm or refute behavioral claims | Builds are slow and repository code is untrusted. Use clean checkouts, explicit time limits, no network by default, and an opt-in command policy. |
| 5 | Root-cause deduplication and evidence ranking | Reduce repeated comments and publish the best-supported location | Over-merging distinct bugs. Preserve all source candidates and evidence links for audit. |
| 6 | Adaptive compute and telemetry | Spend limited review calls on risky changes and identify where recall is lost | A learned policy can overfit. Begin with recorded rules, fixed caps, and development-only threshold tuning. |

### 1. Construct a deterministic change map

Parse the patch into changed symbols and semantic units before calling the reviewer. For each unit, assemble a bounded context pack with:

- the base and head versions of the changed symbol, including enough surrounding control flow to understand the edit;
- directly referenced definitions, imports, interface or type declarations, callers and callees found by exact symbol search, and nearby tests;
- changed configuration, schema, migration, generated-code markers, and repository review instructions;
- a manifest naming every selected snippet, its revision, selection reason, byte or token count, and any omitted candidate.

Start with portable mechanisms already close to the Go CLI: Git object reads, repository file enumeration, exact identifier search, import parsing, and small language-specific extractors. This preserves Linux, macOS, Windows, amd64, and arm64 builds. Make richer AST, language-server, or CodeQL context optional until an ablation shows value. Aider's repository map is a useful implementation precedent: it sends symbol signatures and dependency-ranked files within a token budget. Its documentation is not controlled review-quality evidence. [Aider repository map](https://aider.chat/docs/repomap.html)

Replace byte packing with units grouped by changed symbol and dependency edges. Each unit should carry the relevant patch hunks plus its context pack. Shared dependencies can be referenced by content hash and reused. Keep a hard upper bound and report coverage rather than silently sending a partial unit.

### 2. Separate discovery from verification

The discovery stage should emit small atomic candidates with a suspected root cause, consequence, changed anchor, relevant symbols, and requested evidence. It should favor recall and may combine:

- deterministic patterns, such as a changed return value ignored by a caller, reordered mutation and validation, interface changes without updated implementers, or schema changes without migration coverage;
- static diagnostics introduced between base and head;
- one or more model discovery passes over semantic units.

The verifier should receive one candidate or a small related group plus deterministic context. It must compare base and head behavior, inspect callers and tests, and record supporting and opposing evidence. It returns `confirmed`, `rejected`, or `unresolved`. A confirmed finding needs a causal link to the change and an actionable consequence. The production verifier must never receive benchmark reference findings, held-out labels, or competitor outputs.

Keep verification targeted. The ten-case sample has little evidence of a product-validity problem, so rejecting all weakly worded candidates would likely sacrifice the recall the project needs to improve. Use deterministic contradictions, failed evidence requirements, and reproducible checks rather than the reviewer's self-reported confidence.

### 3. Add PR-level synthesis and publication-aware anchoring

After unit review, build a PR-level graph of candidates, symbols, and evidence. The synthesis stage can join a changed callee in one chunk to a caller in another, merge duplicate root causes, and decide which evidence is strongest. It should not publish a new defect without sending that candidate through verification.

Separate issue validity from comment placement. If a valid issue is caused by the patch but its clearest evidence lies off-hunk, try to re-anchor it to the changed line that introduced the behavior. If no truthful inline anchor exists, publish it as a PR-level finding or leave it explicitly unresolved. Do not discard the issue solely because an evidence line is unchanged.

Use a stable root-cause fingerprint such as `rule-or-category + changed symbol + normalized causal relation + consequence`, with a versioned algorithm. Store source-specific identities and provenance as well. SARIF standardizes results, code flows, provenance, ranks, and partial fingerprints, and GitHub uses partial fingerprints to prevent duplicate code-scanning alerts. SARIF also warns that ranks from different tools are not automatically comparable. [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/sarif-v2.1.0-os.html), [GitHub SARIF support](https://docs.github.com/en/code-security/reference/code-scanning/sarif-files/sarif-support)

Rank within DiffVouch by verification state, severity, causal proximity to the patch, reproducibility, and evidence completeness. Keep benchmark match status out of production ranking.

### 4. Integrate analyzers and tests as evidence providers

Define a common diagnostic input with tool, rule, message, base/head location, code flow, severity, confidence or precision when the source provides it, execution status, and logs. Accept SARIF and simple adapters for compiler, linter, type-checker, and test output. Diff base and head diagnostics so an existing warning does not become a claimed regression.

Run the cheapest checks first:

1. syntax and type checks scoped to changed packages or projects;
2. repository-configured linters and focused tests associated with changed paths;
3. broader tests or interprocedural analyzers only for candidates that need them.

CodeQL path queries can provide source-to-sink paths across functions, but GitHub documents that global data flow costs more time and memory and can be less precise than local analysis. It should be an optional evidence provider, not a mandatory dependency. [CodeQL data-flow documentation](https://codeql.github.com/docs/writing-codeql-queries/about-data-flow-analysis/), [path-query documentation](https://codeql.github.com/docs/writing-codeql-queries/creating-path-queries/)

Do not infer that a passing test proves a candidate false. Record what the test covers and whether it ran on the pinned head. A failed check is evidence only after distinguishing a change-induced failure from setup, dependency, or fixture failure.

### Maintainability needs explicit candidates

Extend discovery to flag newly duplicated business rules, branching or nesting that obscures an invariant, and new abstractions whose callers cannot use their generality. Compare base and head before proposing a finding. Require a concrete consequence, such as the same rule now needing updates in multiple places, inconsistent behavior between duplicated paths, or a state transition that cannot be tested independently. A complexity count alone is not a finding, and naming or formatting preferences do not qualify.

Keep maintainability findings separate from benchmark-reference matches. A useful simplification may have no reference comment and can therefore lower reference precision even when it benefits a maintainer. Validate these candidates on the personal development PRs without assuming those PRs are good.

### 5. Add adaptive compute after the stages are measurable

Begin with transparent risk signals: number of changed semantic units, dependency fan-in and fan-out, public API changes, authentication or authorization code, persistence and migrations, concurrency, error handling, dependency or CI configuration, and absence of related tests. A small local change can use one discovery and one verification pass. A cross-cutting or high-risk change can receive more context hops, specialized analyzers, or a second independent discovery pass.

Set review-wide limits for model calls, wall time, tool calls, analyzer time, context bytes, and candidate count. When a limit is reached, report uncovered units and unresolved candidates. Do not hide them behind a complete status. This approach needs its own experiment. Adaptive-RAG showed that routing retrieval effort by question complexity can improve efficiency and accuracy in open-domain QA, but that is not code-review evidence. [NAACL 2024 paper](https://aclanthology.org/2024.naacl-long.389/)

## Telemetry and error taxonomy

Persist a private trace for each review with content hashes by default, not source text. Record:

- eligible, reviewed, excluded, binary, and omitted files, hunks, symbols, and bytes;
- each semantic unit, retrieval candidate, selected context item, reason, hop count, revision, size, and truncation;
- commands, analyzers, tests, exit status, timeout, output truncation, and base/head diagnostic delta;
- candidates generated, verified, rejected, unresolved, merged, re-anchored, and published, with provenance edges;
- stage latency, model and tool call counts, input/output usage when the provider supplies it, and the budget stop reason.

Use separate status dimensions instead of one error string:

- `fixture`: checkout, patch mismatch, unavailable revision, merge-base reconstruction;
- `provider`: authentication, quota, rate limit, incomplete response, stream or schema failure;
- `tool`: unavailable executable, timeout, truncation, forbidden operation;
- `analysis`: unsupported language, extraction failure, build/setup failure, analyzer failure;
- `coverage`: omitted file, unit, context, or candidate due to a configured limit;
- `verification`: confirmed, rejected, unresolved, or conflicting evidence;
- `publication`: invalid location, re-anchored, PR-level only, deduplicated, or render failure.

This taxonomy makes completion rate, review quality, and infrastructure reliability separately measurable.

## Fair experiment plan

1. Treat the ten inspected Martian cases as exploratory. Do not tune thresholds from their reference labels, and do not pool them into a final untouched confirmation after using their outcomes to choose the architecture.
2. Implement telemetry first and replay existing artifacts where possible. Fix fixture reconstruction and provider-resume reliability as separate work. Neither change should count as a review-quality win.
3. Use only the preselected personal development cohort for architectural tuning. Labels, expected findings, and archived competitor outputs stay outside the production review process.
4. Run paired development-cohort ablations with the same reviewer, effort, judge, case revisions, and budgets: current tools baseline; deterministic context only; context plus semantic units and PR synthesis; then discovery plus verifier; then analyzer/test evidence; then adaptive compute. Change one architectural component at a time.
5. Repeat the fixed development subset enough to estimate run variation. Report review calls, elapsed time, completion rate, coverage, context omissions, candidates at each disposition, analyzer availability, judged validity, unresolved findings, valid additional issues, and concrete maintainability findings. Add reference F2, coverage, and precision only in the later labeled confirmation.
6. Freeze retrieval rules, risk thresholds, budgets, schemas, and deduplication before one confirmation run on untouched cases selected without seeing outcomes. Use paired PR resampling and repository-stratified results. Audit a blinded sample of gained, lost, unmatched-valid, invalid, and unresolved findings with a human reviewer because the automatic judge is another model.
7. Adopt an architectural change only if the result improves the chosen quality measure without an unacceptable loss in completion, latency, or subscription use. Report confidence intervals and regressions by repository and issue category. A positive point estimate with an interval crossing zero remains inconclusive.

## Cost and subscription constraints

Deterministic Git reads, text retrieval, parsers, fingerprints, and local diagnostic ingestion use no model allowance. They add CPU, disk, and implementation cost. Tests and analyzers can dominate wall time and may execute untrusted repository code, so the product needs explicit isolation and network policy before enabling them by default.

Every additional discovery, verification, or synthesis call consumes the same ChatGPT subscription allowance that already gates evaluation requests. Cache context by repository revision and candidate fingerprints, resume at stage boundaries, and prefer deterministic rejection before another model call. Never fall back to a billable API when subscription allowance is exhausted.

CodeQL has meaningful license and deployment constraints. GitHub permits the CLI for public repositories and certain research uses, while private automated analysis requires the applicable GitHub Code Security license. The CLI also requires separate installation and is not compatible with musl-based Linux distributions such as Alpine. [CodeQL CLI documentation](https://docs.github.com/en/code-security/concepts/code-scanning/codeql/codeql-cli), [CodeQL terms](https://github.com/github/codeql-cli-binaries/blob/main/LICENSE.md)

Keep the first production increment self-contained and cross-platform. Optional language servers, AST packs, CodeQL, or hosted indexes should prove incremental value in ablation runs before becoming installation requirements or paid dependencies.
