# Running review evaluations

`diffvouch eval` evaluates the native Go subscription reviewer. It does not launch Codex, Claude Code, or competitor products, and it has no billable API fallback. The [design](evals.md) records the agreed scope and interpretation rules.

## Frozen corpus

`diffvouch eval prepare --owner divyangchauhan` downloads Martian's 50 PRs and archived reviews, all 350 SWE-PRBench cases with the published 100-case split marked, and public PRs from the owner's repositories. It pins dataset revisions and PR base/head commits in `.diffvouch-evals/manifest.json`. Repeating preparation requires a new directory; it cannot silently replace the frozen manifest.

The September 19, 2026 inventory contains 610 cases. Thirty personal PRs are development cases selected by a stable hash before reviewing outputs. The other 580 cases are evaluation cases: 50 Martian, 350 SWE-PRBench, and 180 personal PRs. Do not tune reviewer prompts against these evaluation outputs. Use `eval run --cohort development --tools none` for prompt experiments. Development runs have separate records and never enter held-out scores. Dataset updates belong in a new corpus directory.

The manifest includes 2,449 historical competitor reviews. Each archive is checked against the selected Martian mirror's base/head commits before grading. A mismatch or unavailable mirror is a failure, not a comparable clean review. SWE-PRBench's checkout diff must match the stored dataset patch, ignoring index hash abbreviations.

Downloaded source files are cached. Original Martian GPT-5.2 candidate, deduplication, and evaluation files are retained under `sources/martian/`, with source URLs and SHA-256 hashes in the manifest. These are original judge outputs, not current product measurements. Martian data/code uses MIT; SWE-PRBench data is attributed to FoundryHQ-AI under CC-BY-4.0. Source attribution remains in every generated report.

## Review and grading

```sh
diffvouch eval run --tools none
diffvouch eval run --suite martian --phase grade --tools all
diffvouch eval report
```

The first command reviews and judges all evaluation cases using Sol with high reasoning. The second grades archived Martian outputs alongside the saved DiffVouch reviews. Running `eval run` without flags does both in one pass. Every command resumes existing checkpoints. `--limit` limits visited cases, including already completed cases; omit it for the full corpus. `--retry-errors` retries recorded failures while retaining completed review output.

`--phase review` saves reviews without grading. `--phase grade` grades saved DiffVouch reviews and selected archives; it does not generate missing DiffVouch reviews. `--tools` selects archived tool names, `all`, or `none`. Competitor products are never invoked.

The common judge extracts atomic claims, deduplicates root causes, validates evidence, and matches reference findings. DiffVouch provider metadata and explicit tool names are omitted from judge requests; comment wording and output format can still reveal their origin. The judge has repository tools, and this is not a hardened blind experiment. Separate Sol sessions judge each review. Unresolved or valid unmatched findings trigger a fresh Astra adjudication, with the same rule for all tools. Both judgments remain in the record. `--adjudicator ''` disables escalation and creates a different run configuration.

Correctness, security, and concrete maintainability benefits count. Taste and formatting preferences do not. A valid additional finding still counts as unmatched under strict reference scoring; its separate validity assessment preserves its benefit. Martian core excludes style/speculative references. SWE human-reference coverage is not exhaustive bug recall. Personal PRs have no reference precision, recall, or F2 score. An unresolved finding is neither verified valid nor verified invalid.

## Subscription budget

Every model request checks the included subscription allowance. Missing usage information, an exhausted window, or a provider rate/usage limit pauses the run. Checks do not treat a purchased credit balance as permission to continue. Reviews and initial judgments are saved before later judging stages. A request interrupted inside a review may need to repeat that review; completed reviews are retained.

The approved September reset can be used with:

```sh
diffvouch eval run --tools none \
  --use-reset-expiring-before 2026-09-22T00:00:00Z
```

This authorizes only an available, applicable earned Codex reset expiring before the specified UTC cutoff. It waits until exhaustion, persists an idempotency key before redemption, then resumes checkpoints. It does not purchase credits or consume resets with later expirations. Without the flag, exhaustion stops the run. A paused run does not wait indefinitely for a future automatic weekly reset.

## Reports and changes between runs

Each run records the executable hash, default reviewer prompt hash, judge prompt hash, manifest hash, models, reasoning effort, and repeat number. Repository instructions are pinned by each case's base revision. Changing code, prompts, or model settings creates a new run directory. Keep the previous executable if you need to resume that exact baseline.

`report.json` and `report.md` show per-suite and per-tool expected/completed/failed/pending coverage, reference counts, validity, concrete maintainability findings, and unresolved findings. JSON also includes per-repository scores, adjudication counts, and how often adjudication changed the score counts. Record files preserve evidence and errors. Clean temporary checkouts are removed after each case to bound disk usage; their commits remain reproducible from the manifest. Paused or modified checkouts are retained.

Martian comparisons use only shared completed PRs, with deterministic paired PR bootstrap intervals. Intervals require at least two shared cases; small samples do not support confident rankings. The 50 PRs come from five projects, so repository dependence and judge error limit what these intervals establish. Missing or failed cases must not be treated as clean reviews. SWE repository-access runs differ from its published frozen-context model baselines and cannot establish a directly comparable product ranking.

```sh
diffvouch eval run --repeat 1 --tools none
diffvouch eval compare --before /path/to/earlier/run --after /path/to/later/run
```

A nonzero repeat selects the same deterministic approximately 20% subset for independent runs. `compare` requires the same manifest and writes observations on shared completed cases to `comparison.json`. Use it for code/prompt comparisons and repeat variation. Changes to the judge also change the measurement and should be assessed separately. No command enforces a regression threshold or CI quality gate.
