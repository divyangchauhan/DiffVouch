from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Literal

import yaml
from platformdirs import user_config_path
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from diffvouch.errors import ArgumentError
from diffvouch.git import GitError, run_git
from diffvouch.rating import DEFAULT_WEIGHTS, validate_weights


class ConfigModel(BaseModel):
    model_config = ConfigDict(extra="forbid")


class ProviderModels(ConfigModel):
    codex: str | None = None
    claude: str | None = None


class ProviderConfig(ConfigModel):
    # Repository configuration must never opt a user into billable API usage.
    default_transport: Literal["cli"] = "cli"
    models: ProviderModels = Field(default_factory=ProviderModels)


class ReviewConfig(ConfigModel):
    rubric: dict[str, int] = Field(default_factory=lambda: dict(DEFAULT_WEIGHTS))
    instructions: list[str] = Field(default_factory=list)
    exclude: list[str] = Field(default_factory=list)
    max_diff_bytes: int = Field(default=500_000, ge=10_000)
    chunk_bytes: int = Field(default=180_000, ge=10_000)


class QualityGateConfig(ConfigModel):
    fail_below: float | None = Field(default=None, ge=1, le=5)
    fail_on_severity: Literal["critical", "high", "medium", "low"] | None = None


class RepositoryConfig(ConfigModel):
    version: int = 1
    provider: ProviderConfig = Field(default_factory=ProviderConfig)
    review: ReviewConfig = Field(default_factory=ReviewConfig)
    quality_gate: QualityGateConfig = Field(default_factory=QualityGateConfig)


def load_repository_config(
    repository: Path,
    explicit: str | None = None,
    *,
    trusted_ref: str = "HEAD",
) -> RepositoryConfig:
    path = Path(explicit).expanduser() if explicit else None
    try:
        if path is not None:
            if not path.exists():
                return RepositoryConfig()
            contents = path.read_text(encoding="utf-8")
            source = str(path)
        else:
            try:
                contents = run_git(repository, "show", f"{trusted_ref}:.diffvouch.yml")
            except GitError:
                return RepositoryConfig()
            source = f"{trusted_ref}:.diffvouch.yml"
        raw = yaml.safe_load(contents) or {}
        config = RepositoryConfig.model_validate(raw)
        if config.version != 1:
            raise ArgumentError(f"unsupported configuration version {config.version}")
        validate_weights(config.review.rubric)
        return config
    except (OSError, yaml.YAMLError, ValidationError, ValueError) as exc:
        raise ArgumentError(f"invalid DiffVouch configuration {source}: {exc}") from exc


def config_directory() -> Path:
    return Path(user_config_path("diffvouch", appauthor=False))


def global_config_path() -> Path:
    return config_directory() / "config.json"


def load_global_config() -> dict[str, Any]:
    path = global_config_path()
    if not path.exists():
        return {"version": 1, "api_keys": {}, "github_apps": {}}
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ArgumentError(f"cannot read global configuration {path}: {exc}") from exc
    if not isinstance(value, dict) or value.get("version") != 1:
        raise ArgumentError(f"unsupported global configuration in {path}")
    value.setdefault("api_keys", {})
    value.setdefault("github_apps", {})
    return value


def save_global_config(config: dict[str, Any]) -> None:
    directory = config_directory()
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    try:
        directory.chmod(0o700)
    except OSError:
        pass
    path = global_config_path()
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(config, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)
    path.chmod(0o600)
