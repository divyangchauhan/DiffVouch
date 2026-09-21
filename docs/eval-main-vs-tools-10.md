# Main versus repository access: ten PRs

Completed 2026-09-21. This is a paired diagnostic run on ten Martian PRs, with two PRs from each of Discourse, Cal.com, Keycloak, Grafana, and Sentry.

The repository-tools version scored **+5.8 percentage points** higher on reference F2. The paired PR bootstrap interval is [-5.6, +15.2] points. This small sample measures the combined change; it cannot isolate file reading or establish a general improvement.

| Measure | Main, patch only | Repository tools enabled |
|---|---:|---:|
| PRs graded | 10 | 10 |
| Benchmark issues matched | 13 | 15 |
| Benchmark issues missed | 19 | 17 |
| Claims without a reference match | 16 | 16 |
| Reference precision | 44.8% | 48.4% |
| Reference coverage | 40.6% | 46.9% |
| Reference F2 | 41.4% | 47.2% |
| Findings judged valid | 28 | 31 |
| Findings judged invalid | 1 | 1 |
| Unresolved findings | 0 | 0 |
| Valid maintainability findings | 0 | 0 |

Neither version produced a finding judged to provide a concrete maintainability benefit in these ten PRs. This sample therefore does not establish an improvement in code simplicity or cleanliness.

The interval includes zero, so this sample does not establish a reliable F2 improvement.

A claim without a reference match is a false positive for reference F2 even when the judge considers it a valid additional issue. Finding validity is therefore reported separately. Style and speculative reference issues are excluded; only concrete benefits qualify as valid maintainability findings. These are model judgments, not a human audit.

## Versions and controls

- Main review implementation: `119d547c45a6138f2f2610d86d1ce6dee86eef93`, fetched as `origin/main` before the run. Its original prompt and patch-only behavior were retained, with one request per patch chunk.
- Repository-tools implementation: commit `543c5c38fe8599cec2e03318f1499150e5f1749c`, in the previously frozen executable built from `3e57e09cf96b89277ec4a459c7fa67458c4713bc` plus the subscription/evaluation support subsequently committed as `6c4c6454bae8d47c9abff3233002923645c47780`.
- Main required an isolated compatibility adapter because that version had no native subscription or eval command. The adapter adds authentication, usage checks, and the evaluation runner. It advertises no reviewer tools. The judge retains the same repository tools used in the saved run.
- Both use `gpt-5.6-sol` with high reasoning for reviews and initial grading. The unchanged adjudication rule uses `gpt-6-astra` for unresolved findings and valid findings without reference matches. Neither invokes Codex or Claude Code.
- The tool-enabled reviews and judgments were reused from run `f003bd8b42da24a9`; only main was newly evaluated. One sample per PR per version, with no prompt tuning or regression gate.
- Main run: `e849423ae32486c1`. Reviewer prompt hash `0612d2c75f7af2b5dc5a1ca40ee18640a0bc8ace8fbbea23178943979a66d1d7`. Shared judge prompt hash `f96bb4db4e819f2fb7fca4f8bf15b2ef9e6388f18d2cdbf9683f3c46c2987b1f`.
- Main executable SHA-256: `900e550958a04c9c59219b07562fecfce19b56f64d92391b2a0d76ccb77cfe5d`. Tool-enabled executable SHA-256: `2b64632c2c562325bdd0f6dc1ba2e685019a5735d65689d26de076addcfdfbb3`.

The measured change opens repository file access and Bash and changes the reviewer prompt. It retains the 64 KiB per-tool-output cap with paged reads, as well as the existing patch-size and chunking limits. It is not a test of removing every size limit.

## Selection and case results

The first two completed DiffVouch cases per project in the frozen manifest order were selected before this run. Selection used project and completion status, never scores. Conditioning on completed saved cases excludes prior runtime failures, so this does not measure reliability across the full corpus. Benchmark mirrors provide pinned base/head revisions.

| Benchmark PR | Main matches | Tool-enabled matches | Gained references | Lost references |
|---|---:|---:|---|---|
| [ai-code-review-evaluation/discourse-graphite/pull/3](https://github.com/ai-code-review-evaluation/discourse-graphite/pull/3) | 2 | 2 | None | None |
| [ai-code-review-evaluation/discourse-graphite/pull/7](https://github.com/ai-code-review-evaluation/discourse-graphite/pull/7) | 2 | 2 | None | None |
| [calcom/cal.com/pull/10967](https://github.com/calcom/cal.com/pull/10967) | 1 | 1 | g3 | g4 |
| [keycloak/keycloak/pull/33832](https://github.com/keycloak/keycloak/pull/33832) | 0 | 0 | None | None |
| [grafana/grafana/pull/79265](https://github.com/grafana/grafana/pull/79265) | 1 | 2 | g5 | None |
| [grafana/grafana/pull/106778](https://github.com/grafana/grafana/pull/106778) | 1 | 2 | g2 | None |
| [getsentry/sentry/pull/80528](https://github.com/getsentry/sentry/pull/80528) | 1 | 0 | None | g1 |
| [keycloak/keycloak/pull/32918](https://github.com/keycloak/keycloak/pull/32918) | 1 | 1 | None | None |
| [calcom/cal.com/pull/14943](https://github.com/calcom/cal.com/pull/14943) | 1 | 2 | g1 | None |
| [getsentry/sentry/pull/93824](https://github.com/getsentry/sentry/pull/93824) | 3 | 3 | None | None |

The case-level changes went both ways. In [Grafana #79265](https://github.com/grafana/grafana/pull/79265), the tool-enabled review matched a device-limit bypass caused by caching a rejected device before checking admission. In [Sentry #80528](https://github.com/getsentry/sentry/pull/80528), main matched a return-value bug where transformed configuration was discarded, while the tool-enabled review missed it. Both findings were judged valid. These examples show the kinds of gains and losses, not their causes.

The 95% interval resamples paired PRs 2,000 times with Python `random.Random(1)`. It is conditional on the saved reviews and judgments. It does not capture model/grade variation or dependence between PRs from the same project. Published competitor rankings cannot be inferred from this ten-case experiment.

## Validation and artifacts

`go test -race ./...` and `go vet ./...` passed for the committed implementation and the main compatibility worktree. The original main prompt was checked byte for byte. Adapter tests confirmed one native subscription request per chunk without tools and rejection of unexpected tool calls.

The paired counts and provenance are committed beside this report in `eval-main-vs-tools-10.json`. Full local checkpoints, the selected manifest, exact reused records, logs, executable, and source overlay are under `.diffvouch-evals/main-10/`. `comparison-plan.json` records the overlay checksum. These runtime artifacts remain gitignored. The full campaign remains paused.
