from __future__ import annotations

from pathlib import Path

from diffvouch.config import RepositoryConfig, load_repository_config
from diffvouch.errors import ArgumentError
from diffvouch.git import chunk_patch, collect_diff, repository_root, run_git
from diffvouch.models import (
    Dimensions,
    FilesSummary,
    ProviderInfo,
    ProviderReview,
    Publication,
    ReviewResult,
    Scope,
    Severity,
)
from diffvouch.prompt import build_review_prompt
from diffvouch.providers import Provider, create_provider
from diffvouch.rating import add_finding_ids, calculate_rating, evaluate_gate
from diffvouch.sanitize import redact_secrets


def _combine(reviews: list[ProviderReview], sizes: list[int]) -> ProviderReview:
    if len(reviews) == 1:
        return reviews[0]
    total = sum(sizes)
    dimension_values: dict[str, float] = {}
    for name in Dimensions.model_fields:
        dimension_values[name] = round(
            sum(getattr(review.dimensions, name) * size for review, size in zip(reviews, sizes))
            / total,
            2,
        )
    findings = []
    seen: set[tuple[str | None, int | None, str]] = set()
    for review in reviews:
        for finding in review.findings:
            identity = (finding.path, finding.line, finding.title.casefold())
            if identity not in seen:
                seen.add(identity)
                findings.append(finding)
    positives = list(
        dict.fromkeys(item for review in reviews for item in review.positive_observations)
    )
    verification = list(
        dict.fromkeys(item for review in reviews for item in review.needs_verification)
    )
    summaries = " ".join(review.summary.strip() for review in reviews)
    return ProviderReview(
        summary=f"Reviewed the complete patch in {len(reviews)} chunks. {summaries}",
        dimensions=Dimensions(**dimension_values),
        findings=findings,
        positive_observations=positives,
        needs_verification=verification,
    )


def perform_review(
    *,
    provider_name: str,
    transport: str | None,
    model: str | None,
    effort: str | None,
    base: str | None,
    committed_only: bool,
    staged_only: bool,
    excludes: list[str],
    config_path: str | None,
    max_diff_bytes: int | None,
    fail_below: float | None,
    fail_on_severity: str | None,
    publication_requested: bool,
    root: Path | None = None,
) -> ReviewResult | None:
    repository = repository_root(root)
    trusted_ref = (
        run_git(repository, "rev-parse", "--verify", f"{base}^{{commit}}").strip()
        if base
        else "HEAD"
    )
    config: RepositoryConfig = load_repository_config(
        repository, config_path, trusted_ref=trusted_ref
    )
    selected_transport = transport or "cli"
    selected_model = model or getattr(config.provider.models, provider_name)
    patterns = [*config.review.exclude, *excludes]
    collected = collect_diff(
        root=repository,
        base=base,
        committed_only=committed_only,
        staged_only=staged_only,
        excludes=patterns,
        max_diff_bytes=max_diff_bytes or config.review.max_diff_bytes,
    )
    if not collected.patch:
        return None
    sanitized, redaction_count = redact_secrets(collected.patch)
    chunks = chunk_patch(sanitized, config.review.chunk_bytes)
    provider: Provider = create_provider(provider_name, selected_transport, selected_model, effort)
    reviews: list[ProviderReview] = []
    sizes: list[int] = []
    for index, chunk in enumerate(chunks, start=1):
        prompt = build_review_prompt(
            patch=chunk,
            chunk_index=index,
            chunk_count=len(chunks),
            rubric=config.review.rubric,
            instructions=config.review.instructions,
        )
        reviews.append(provider.review(prompt))
        sizes.append(len(chunk.encode()))
    provider_review = _combine(reviews, sizes)

    reviewed_paths = set(collected.reviewed_files)
    accepted = []
    needs_verification = list(provider_review.needs_verification)
    for finding in provider_review.findings:
        if finding.path is not None and finding.path not in reviewed_paths:
            needs_verification.append(
                f"Provider cited {finding.path!r}, which was not part of the reviewed patch."
            )
            continue
        accepted.append(finding)
    provider_review.findings = accepted
    provider_review.needs_verification = needs_verification
    findings = add_finding_ids(provider_review)
    rating = calculate_rating(provider_review, config.review.rubric)
    selected_fail_below = fail_below if fail_below is not None else config.quality_gate.fail_below
    selected_fail_severity = fail_on_severity or config.quality_gate.fail_on_severity
    try:
        severity = Severity(selected_fail_severity) if selected_fail_severity else None
    except ValueError as exc:
        raise ArgumentError(f"invalid fail-on severity {selected_fail_severity!r}") from exc
    gate = evaluate_gate(rating, findings, selected_fail_below, severity)
    return ReviewResult(
        scope=Scope(
            mode=collected.mode,
            base_ref=collected.base_ref,
            base_sha=collected.base_sha,
            merge_base=collected.merge_base,
            head_sha=collected.head_sha,
        ),
        provider=ProviderInfo(
            name=provider_name,
            transport=selected_transport,
            model=provider.model,
            effort=effort,
        ),
        rating=rating,
        summary=provider_review.summary,
        findings=findings,
        positive_observations=provider_review.positive_observations,
        needs_verification=provider_review.needs_verification,
        files=FilesSummary(
            reviewed=collected.reviewed_files,
            excluded=collected.excluded_files,
            binary=collected.binary_files,
            omitted=[],
            redactions=redaction_count,
        ),
        gate=gate,
        publication=Publication(requested=publication_requested),
    )
