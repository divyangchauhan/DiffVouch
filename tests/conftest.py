from __future__ import annotations

import subprocess
from pathlib import Path

import pytest


@pytest.fixture
def git_repo(tmp_path: Path) -> Path:
    subprocess.run(["git", "init", "-q", "--initial-branch=main"], cwd=tmp_path, check=True)
    subprocess.run(["git", "config", "user.name", "DiffVouch Test"], cwd=tmp_path, check=True)
    subprocess.run(
        ["git", "config", "user.email", "diffvouch@example.invalid"], cwd=tmp_path, check=True
    )
    (tmp_path / "tracked.txt").write_text("original\n", encoding="utf-8")
    subprocess.run(["git", "add", "tracked.txt"], cwd=tmp_path, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "initial"], cwd=tmp_path, check=True)
    return tmp_path
