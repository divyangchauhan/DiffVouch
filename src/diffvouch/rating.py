from __future__ import annotations

from diffvouch.models import Finding, Gate, ProviderReview, Rating, Severity

DEFAULT_WEIGHTS = {
    "correctness": 35,
    "security": 20,
    "maintainability": 20,
    "testing": 15,
    "scope": 10,
}

SEVERITY_ORDER = {
    Severity.LOW: 1,
    Severity.MEDIUM: 2,
    Severity.HIGH: 3,
    Severity.CRITICAL: 4,
}


def rating_label(score: float) -> str:
    if score >= 4.5:
        return "Excellent"
    if score >= 3.5:
        return "Good"
    if score >= 2.5:
        return "Needs work"
    if score >= 1.5:
        return "High risk"
    return "Critical risk"


def calculate_rating(review: ProviderReview, weights: dict[str, int]) -> Rating:
    values = review.dimensions.model_dump()
    score = sum(values[name] * weight for name, weight in weights.items()) / 100
    material = [
        finding
        for finding in review.findings
        if finding.category.value in {"correctness", "security"}
    ]
    if any(finding.severity is Severity.CRITICAL for finding in material):
        score = min(score, 2.4)
    elif any(finding.severity is Severity.HIGH for finding in material):
        score = min(score, 3.4)
    score = round(score + 1e-9, 1)
    return Rating(overall=score, label=rating_label(score), dimensions=review.dimensions)


def add_finding_ids(review: ProviderReview) -> list[Finding]:
    findings: list[Finding] = []
    for index, source in enumerate(review.findings, start=1):
        values = source.model_dump()
        if source.severity in {Severity.CRITICAL, Severity.HIGH}:
            values["blocking"] = True
        elif source.severity is Severity.LOW:
            values["blocking"] = False
        findings.append(Finding(id=f"DV-{index:03d}", **values))
    return findings


def evaluate_gate(
    rating: Rating,
    findings: list[Finding],
    fail_below: float | None,
    fail_on_severity: Severity | None,
) -> Gate:
    reasons: list[str] = []
    if fail_below is not None and rating.overall < fail_below:
        reasons.append(f"rating {rating.overall:.1f} is below {fail_below:.1f}")
    if fail_on_severity is not None:
        threshold = SEVERITY_ORDER[fail_on_severity]
        matched = [
            finding.id for finding in findings if SEVERITY_ORDER[finding.severity] >= threshold
        ]
        if matched:
            reasons.append(f"findings at or above {fail_on_severity.value}: {', '.join(matched)}")
    return Gate(passed=not reasons, reasons=reasons)


def validate_weights(weights: dict[str, int]) -> None:
    if set(weights) != set(DEFAULT_WEIGHTS):
        raise ValueError(
            "rubric must contain correctness, security, maintainability, testing, and scope"
        )
    if any(not isinstance(value, int) or value < 0 for value in weights.values()):
        raise ValueError("rubric weights must be non-negative integers")
    if sum(weights.values()) != 100:
        raise ValueError("rubric weights must total 100")
