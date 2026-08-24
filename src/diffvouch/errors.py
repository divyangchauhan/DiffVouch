from __future__ import annotations


class DiffVouchError(Exception):
    exit_code = 2


class ArgumentError(DiffVouchError):
    exit_code = 2


class GitError(DiffVouchError):
    exit_code = 3


class ProviderError(DiffVouchError):
    exit_code = 4


class GitHubError(DiffVouchError):
    exit_code = 5


class CoverageError(DiffVouchError):
    exit_code = 6
