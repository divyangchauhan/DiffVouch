from __future__ import annotations

from argparse import Namespace
from unittest.mock import patch

from diffvouch.cli import _command_auth_status, build_parser


def test_cli_is_named_diffvouch() -> None:
    assert build_parser().prog == "diffvouch"


def test_review_provider_is_explicit() -> None:
    parser = build_parser()
    args = parser.parse_args(["review", "--provider", "codex"])

    assert args.provider == "codex"
    assert args.transport is None


def test_api_key_makes_provider_auth_status_ready() -> None:
    with (
        patch("diffvouch.cli._auth_status", return_value=(False, "not logged in")),
        patch(
            "diffvouch.cli.load_global_config",
            return_value={"api_keys": {"openai": {"backend": "file", "name": "key"}}},
        ),
    ):
        status = _command_auth_status(Namespace(provider="openai"))

    assert status == 0
