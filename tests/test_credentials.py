from __future__ import annotations

import stat
from pathlib import Path
from unittest.mock import patch

from diffvouch.credentials import delete_secret, read_secret, store_secret


def test_file_secret_storage_is_mode_0600(tmp_path: Path) -> None:
    with patch("diffvouch.credentials.config_directory", return_value=tmp_path):
        reference = store_secret("test", "secret-value", "file")
        path = tmp_path / "secrets.json"

        assert read_secret(reference) == "secret-value"
        assert stat.S_IMODE(path.stat().st_mode) == 0o600
        delete_secret(reference)
        assert "secret-value" not in path.read_text(encoding="utf-8")
