from __future__ import annotations

from enum import StrEnum
from typing import Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field


def _camel(value: str) -> str:
    first, *rest = value.split("_")
    return first + "".join(part.capitalize() for part in rest)


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", alias_generator=_camel, populate_by_name=True)


class Severity(StrEnum):
    CRITICAL = "critical"
    HIGH = "high"
    MEDIUM = "medium"
    LOW = "low"


class Category(StrEnum):
    CORRECTNESS = "correctness"
    SECURITY = "security"
    MAINTAINABILITY = "maintainability"
    TESTING = "testing"
    SCOPE = "scope"


class Confidence(StrEnum):
    HIGH = "high"
    MEDIUM = "medium"
    LOW = "low"


class Dimensions(StrictModel):
    correctness: float = Field(ge=1, le=5)
    security: float = Field(ge=1, le=5)
    maintainability: float = Field(ge=1, le=5)
    testing: float = Field(ge=1, le=5)
    scope: float = Field(ge=1, le=5)


class ProviderFinding(StrictModel):
    severity: Severity
    category: Category
    blocking: bool
    title: str = Field(min_length=1)
    explanation: str = Field(min_length=1)
    recommendation: str = Field(min_length=1)
    path: str | None
    line: int | None = Field(ge=1)
    side: Literal["old", "new"] | None
    confidence: Confidence
    evidence: str = Field(min_length=1)


class ProviderReview(StrictModel):
    summary: str = Field(min_length=1)
    dimensions: Dimensions
    findings: list[ProviderFinding]
    positive_observations: list[str]
    needs_verification: list[str]


class Finding(ProviderFinding):
    id: str


class Scope(StrictModel):
    mode: str
    base_ref: str
    base_sha: str
    merge_base: str | None
    head_sha: str


class ProviderInfo(StrictModel):
    name: Literal["codex", "claude"]
    transport: Literal["cli", "api"]
    model: str
    effort: str | None


class Rating(StrictModel):
    overall: float = Field(ge=1, le=5)
    label: str
    dimensions: Dimensions


class FilesSummary(StrictModel):
    reviewed: list[str]
    excluded: list[str]
    binary: list[str]
    omitted: list[str]
    redactions: int = 0


class Gate(StrictModel):
    passed: bool
    reasons: list[str]


class Publication(StrictModel):
    requested: bool
    published: bool = False
    url: str | None = None
    bot: str | None = None


class ReviewResult(StrictModel):
    schema_version: Literal[1] = 1
    review_id: str = Field(default_factory=lambda: str(uuid4()))
    status: Literal["complete"] = "complete"
    partial: Literal[False] = False
    scope: Scope
    provider: ProviderInfo
    rating: Rating
    summary: str
    findings: list[Finding]
    positive_observations: list[str]
    needs_verification: list[str]
    files: FilesSummary
    gate: Gate
    publication: Publication
