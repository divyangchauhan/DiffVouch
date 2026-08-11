# DiffVouch Review Contract

Apply this contract to every review so results remain comparable across models and harnesses.

## Evidence standard

Report a finding only when all of the following are true:

1. The selected diff introduces, exposes, or materially worsens the issue.
2. A concrete input, state, or execution path can trigger the problem.
3. The impact is meaningful enough that the author would reasonably change the patch.
4. The finding can cite a changed file and the smallest relevant changed-line range.

Do not report speculative architecture concerns, subjective style preferences, generic best practices, or lint that a formatter would handle. When evidence is incomplete but the risk is plausible and material, place it under **Needs verification** rather than **Findings**.

## Severity

- **Critical**: Likely remote compromise, privilege bypass, irreversible widespread data loss, secret exposure, or a release-blocking failure affecting most users.
- **High**: A reproducible major functional failure, exploitable security weakness, significant data corruption, or breaking public behavior without a safe fallback.
- **Medium**: A concrete defect affecting a limited path, important missing error handling, meaningful regression, or missing tests around risky changed behavior.
- **Low**: A small but real correctness, maintainability, test, or scope problem worth fixing before merge.

Severity represents impact and likelihood together. Do not inflate severity to make the review appear useful.

## Categories and weights

Score each dimension from `1.0` through `5.0`:

| Dimension | Weight | Evaluate |
|---|---:|---|
| Correctness | 35% | Logic, edge cases, error paths, concurrency, and behavioral regressions |
| Security | 20% | Trust boundaries, authorization, injection, unsafe data handling, and secrets |
| Maintainability | 20% | Comprehensibility, unnecessary complexity, duplication, and architectural fit |
| Testing | 15% | Coverage of changed behavior and realistic regression paths |
| Scope | 10% | Focus, accidental changes, compatibility, and generated or unrelated files |

Calculate the overall rating as:

```text
correctness*0.35 + security*0.20 + maintainability*0.20 + testing*0.15 + scope*0.10
```

Round only the final result to one decimal place.

Apply these deterministic caps after calculating the weighted result:

- Any critical correctness or security finding caps the overall rating at `2.4`.
- Any high correctness or security finding caps the overall rating at `3.4`.

Score meanings:

- `4.5-5.0`: Excellent; no meaningful concerns found.
- `3.5-4.4`: Good; minor improvements recommended.
- `2.5-3.4`: Needs work; one or more material concerns.
- `1.5-2.4`: High risk; substantial fixes recommended.
- `1.0-1.4`: Critical risk; should not merge as written.

Do not subtract points twice for one root cause. Score uncertainty conservatively: lack of repository context is not itself a defect, but missing tests visible in the patch can lower Testing.

## Finding format

Every finding must contain:

- Severity and category.
- Changed file and changed line or minimal changed-line range.
- A concise title phrased as the defect, not a suggestion.
- Trigger: the concrete input, state, or sequence that exposes it.
- Impact: what fails or becomes unsafe.
- Recommendation: the smallest credible direction for fixing it.
- Confidence: high or medium.

Sort findings by severity, then by file and line. Merge findings with the same root cause. Do not put low-confidence claims in Findings.

## Required output

Return exactly this structure in Markdown. Omit the **Needs verification** section when empty. Never omit the score table.

```markdown
# DiffVouch Review

**Scope:** <working-tree | staged | merge-base(base)..working-tree | merge-base(base)..HEAD>
**Review isolation:** <isolated subagent | fresh process | shared context>
**Model:** <actual model identifier | unknown (inherited)>
**Effort:** <actual effort level | unknown (inherited)>
**Rating:** <x.x>/5 — <Excellent | Good | Needs work | High risk | Critical risk>
**Files reviewed:** <count>

## Findings

### [<severity>][<category>] <title>

`path/to/file.ext:<line-or-range>`

- **Trigger:** <concrete trigger>
- **Impact:** <observable consequence>
- **Recommendation:** <smallest credible fix direction>
- **Confidence:** <high | medium>

<Repeat for each finding, or write "No actionable findings." when empty.>

## Needs verification

- `[category] path/to/file.ext:<line>` — <uncertain material risk and what would confirm it>

## Rating breakdown

| Dimension | Score | Reason |
|---|---:|---|
| Correctness | x.x/5 | <evidence-based reason> |
| Security | x.x/5 | <evidence-based reason> |
| Maintainability | x.x/5 | <evidence-based reason> |
| Testing | x.x/5 | <evidence-based reason> |
| Scope | x.x/5 | <evidence-based reason> |

## Assumptions and validation

- <material assumption, skipped content, recommended validation command, or reason shared context was used>
```

Keep the report concise. Do not add praise merely to balance criticism. Positive observations belong only in a score-table reason when they explain the score.
