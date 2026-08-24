from __future__ import annotations

from diffvouch.models import (
    Category,
    Confidence,
    Dimensions,
    ProviderFinding,
    ProviderReview,
    Severity,
)
from diffvouch.rating import DEFAULT_WEIGHTS, add_finding_ids, calculate_rating, evaluate_gate


def provider_review(severity: Severity = Severity.LOW) -> ProviderReview:
    return ProviderReview(
        summary="Review summary",
        dimensions=Dimensions(
            correctness=5,
            security=5,
            maintainability=5,
            testing=5,
            scope=5,
        ),
        findings=[
            ProviderFinding(
                severity=severity,
                category=Category.CORRECTNESS,
                blocking=False,
                title="Problem",
                explanation="The behavior is incorrect.",
                recommendation="Correct it.",
                path="app.py",
                line=2,
                side="new",
                confidence=Confidence.HIGH,
                evidence="The new branch returns the wrong value.",
            )
        ],
        positiveObservations=[],
        needsVerification=[],
    )


def test_high_correctness_finding_caps_rating_and_becomes_blocking() -> None:
    review = provider_review(Severity.HIGH)

    rating = calculate_rating(review, DEFAULT_WEIGHTS)
    findings = add_finding_ids(review)

    assert rating.overall == 3.4
    assert findings[0].blocking is True
    assert findings[0].id == "DV-001"


def test_quality_gate_checks_rating_and_severity() -> None:
    review = provider_review(Severity.MEDIUM)
    rating = calculate_rating(review, DEFAULT_WEIGHTS)
    findings = add_finding_ids(review)

    gate = evaluate_gate(rating, findings, 4.0, Severity.MEDIUM)

    assert gate.passed is False
    assert any("DV-001" in reason for reason in gate.reasons)


def test_public_schema_uses_camel_case() -> None:
    schema = ProviderReview.model_json_schema()

    assert "positiveObservations" in schema["properties"]
    assert "positive_observations" not in schema["properties"]


def test_provider_schema_requires_every_declared_property() -> None:
    schema = ProviderReview.model_json_schema()

    def assert_strict_object(value: object) -> None:
        if isinstance(value, dict):
            if value.get("type") == "object" and "properties" in value:
                assert set(value["required"]) == set(value["properties"])
            for nested in value.values():
                assert_strict_object(nested)
        elif isinstance(value, list):
            for nested in value:
                assert_strict_object(nested)

    assert_strict_object(schema)
