from __future__ import annotations

import json

from diffvouch.models import Finding, ReviewResult


def render_json(result: ReviewResult) -> str:
    return (
        json.dumps(result.model_dump(mode="json", by_alias=True), indent=2, sort_keys=True) + "\n"
    )


def _finding(finding: Finding) -> list[str]:
    location = ""
    if finding.path:
        location = finding.path
        if finding.line:
            location += f":{finding.line}"
        location = f" — {location}"
    kind = "BLOCKING" if finding.blocking else "NON-BLOCKING"
    return [
        f"[{kind}] [{finding.severity.value.upper()}] {finding.id}: {finding.title}{location}",
        f"  {finding.explanation}",
        f"  Recommendation: {finding.recommendation}",
    ]


def render_terminal(result: ReviewResult) -> str:
    lines = [
        "DiffVouch Review",
        "=" * 16,
        f"Rating: {result.rating.overall:.1f}/5 — {result.rating.label}",
        f"Scope: {result.scope.mode} ({result.scope.base_ref})",
        (
            f"Provider: {result.provider.name}/{result.provider.transport} "
            f"model={result.provider.model}"
        ),
        f"Summary: {result.summary}",
        "",
        "Rating breakdown",
    ]
    for name, score in result.rating.dimensions.model_dump().items():
        lines.append(f"  {name:<16} {score:.1f}/5")

    blocking = [finding for finding in result.findings if finding.blocking]
    non_blocking = [finding for finding in result.findings if not finding.blocking]
    for title, findings in (("Blocking issues", blocking), ("Non-blocking issues", non_blocking)):
        lines.extend(["", title])
        if not findings:
            lines.append("  None")
        for finding in findings:
            lines.extend(_finding(finding))

    if result.needs_verification:
        lines.extend(["", "Needs verification"])
        lines.extend(f"  - {item}" for item in result.needs_verification)
    if result.positive_observations:
        lines.extend(["", "Positive observations"])
        lines.extend(f"  - {item}" for item in result.positive_observations)

    lines.extend(
        [
            "",
            f"Files reviewed: {len(result.files.reviewed)}",
            f"Gate: {'passed' if result.gate.passed else 'failed'}",
        ]
    )
    if result.gate.reasons:
        lines.extend(f"  - {reason}" for reason in result.gate.reasons)
    if result.publication.published:
        lines.append(f"Published by {result.publication.bot}: {result.publication.url}")
    elif result.publication.requested:
        lines.append("GitHub publication requested but not completed.")
    else:
        lines.append("Not published (use --publish to post a GitHub App review).")
    return "\n".join(lines) + "\n"
