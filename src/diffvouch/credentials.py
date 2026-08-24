from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path

import keyring
from keyring.errors import KeyringError

from diffvouch.config import config_directory
from diffvouch.errors import ArgumentError

SERVICE_NAME = "diffvouch"


@dataclass(frozen=True)
class SecretReference:
    name: str
    backend: str

    def as_dict(self) -> dict[str, str]:
        return {"name": self.name, "backend": self.backend}

    @classmethod
    def from_dict(cls, value: dict[str, str]) -> SecretReference:
        return cls(name=value["name"], backend=value["backend"])


def _secret_path() -> Path:
    return config_directory() / "secrets.json"


def _read_file_secrets() -> dict[str, str]:
    path = _secret_path()
    if not path.exists():
        return {}
    mode = path.stat().st_mode & 0o777
    if os.name != "nt" and mode & 0o077:
        raise ArgumentError(f"refusing insecure secret file {path}; expected mode 0600")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ArgumentError(f"cannot read secret file {path}: {exc}") from exc
    if not isinstance(value, dict) or not all(
        isinstance(key, str) and isinstance(secret, str) for key, secret in value.items()
    ):
        raise ArgumentError(f"invalid secret file {path}")
    return value


def _write_file_secrets(secrets: dict[str, str]) -> None:
    directory = config_directory()
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    try:
        directory.chmod(0o700)
    except OSError:
        pass
    path = _secret_path()
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(secrets, sort_keys=True) + "\n", encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)
    path.chmod(0o600)


def _keyring_available() -> bool:
    try:
        return keyring.get_keyring().priority > 0
    except (KeyringError, RuntimeError):
        return False


def store_secret(name: str, secret: str, backend: str = "auto") -> SecretReference:
    if not secret:
        raise ArgumentError("secret cannot be empty")
    selected = backend
    if selected == "auto":
        selected = "keyring" if _keyring_available() else "file"
    if selected == "keyring":
        try:
            keyring.set_password(SERVICE_NAME, name, secret)
        except KeyringError as exc:
            raise ArgumentError(f"OS credential store rejected the secret: {exc}") from exc
    elif selected == "file":
        secrets = _read_file_secrets()
        secrets[name] = secret
        _write_file_secrets(secrets)
    else:
        raise ArgumentError("secret backend must be auto, keyring, or file")
    return SecretReference(name=name, backend=selected)


def read_secret(reference: SecretReference) -> str:
    if reference.backend == "keyring":
        try:
            value = keyring.get_password(SERVICE_NAME, reference.name)
        except KeyringError as exc:
            raise ArgumentError(f"cannot read OS credential store: {exc}") from exc
    elif reference.backend == "file":
        value = _read_file_secrets().get(reference.name)
    else:
        raise ArgumentError(f"unknown secret backend {reference.backend}")
    if not value:
        raise ArgumentError(f"stored secret {reference.name!r} is missing")
    return value


def delete_secret(reference: SecretReference) -> None:
    if reference.backend == "keyring":
        try:
            keyring.delete_password(SERVICE_NAME, reference.name)
        except (KeyringError, keyring.errors.PasswordDeleteError):
            return
    elif reference.backend == "file":
        secrets = _read_file_secrets()
        if reference.name in secrets:
            del secrets[reference.name]
            _write_file_secrets(secrets)
