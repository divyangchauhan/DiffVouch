from __future__ import annotations

import fnmatch
import re
import shlex
import stat
import subprocess
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

from diffvouch.errors import CoverageError, GitError


@dataclass
class CollectedDiff:
    repository: Path
    patch: str
    mode: str
    base_ref: str
    base_sha: str
    merge_base: str | None
    head_sha: str
    reviewed_files: list[str] = field(default_factory=list)
    excluded_files: list[str] = field(default_factory=list)
    binary_files: list[str] = field(default_factory=list)


def run_git(
    repository: Path,
    *args: str,
    allow_diff: bool = False,
    max_output_bytes: int | None = None,
) -> str:
    command = ["git", "-c", "core.quotepath=false", "-c", "color.ui=false", *args]
    try:
        if max_output_bytes is None:
            result = subprocess.run(command, cwd=repository, capture_output=True, check=False)
            stdout, stderr, returncode = result.stdout, result.stderr, result.returncode
        else:
            with tempfile.TemporaryFile() as stderr_file:
                process = subprocess.Popen(
                    command, cwd=repository, stdout=subprocess.PIPE, stderr=stderr_file
                )
                chunks: list[bytes] = []
                size = 0
                assert process.stdout is not None
                while chunk := process.stdout.read(65_536):
                    size += len(chunk)
                    if size > max_output_bytes:
                        process.kill()
                        process.wait()
                        raise CoverageError(
                            f"Git patch exceeded the configured {max_output_bytes}-byte safety limit"
                        )
                    chunks.append(chunk)
                returncode = process.wait()
                stderr_file.seek(0)
                stdout, stderr = b"".join(chunks), stderr_file.read()
    except FileNotFoundError as exc:
        raise GitError("Git is not installed") from exc
    valid_codes = {0, 1} if allow_diff else {0}
    if returncode not in valid_codes:
        detail = stderr.decode(errors="replace").strip()
        raise GitError(detail or f"git {' '.join(args)} failed")
    return stdout.decode("utf-8", errors="surrogateescape")


def repository_root(path: Path | None = None) -> Path:
    candidate = (path or Path.cwd()).resolve()
    output = run_git(candidate, "rev-parse", "--show-toplevel").strip()
    if not output:
        raise GitError("current directory is not inside a Git repository")
    return Path(output)


def _head_sha(repository: Path) -> str:
    return run_git(repository, "rev-parse", "HEAD").strip()


def _decode_path(value: str) -> str | None:
    if value == "/dev/null":
        return None
    if value.startswith('"') and value.endswith('"'):
        payload = value[1:-1]
        data = bytearray()
        index = 0
        escapes = {"n": 10, "r": 13, "t": 9, "\\": 92, '"': 34}
        while index < len(payload):
            character = payload[index]
            if character != "\\":
                data.extend(character.encode())
                index += 1
                continue
            index += 1
            escaped = payload[index]
            if escaped in escapes:
                data.append(escapes[escaped])
                index += 1
            elif escaped in "01234567":
                end = index + 1
                while end < min(index + 3, len(payload)) and payload[end] in "01234567":
                    end += 1
                data.append(int(payload[index:end], 8))
                index = end
            else:
                data.extend(escaped.encode())
                index += 1
        value = data.decode("utf-8", errors="surrogateescape")
    return value[2:] if value.startswith(("a/", "b/")) else value


def split_file_patches(patch: str) -> list[str]:
    starts = [match.start() for match in re.finditer(r"(?m)^diff --git ", patch)]
    if not starts:
        return []
    starts.append(len(patch))
    return [patch[starts[index] : starts[index + 1]] for index in range(len(starts) - 1)]


def patch_path(section: str) -> str | None:
    for line in section.splitlines():
        if line.startswith("+++ "):
            new_path = _decode_path(line[4:])
            if new_path is not None:
                return new_path
        if line.startswith("--- "):
            old_path = _decode_path(line[4:])
            if old_path is not None:
                fallback = old_path
    return locals().get("fallback")


def diff_header_path(section: str) -> str | None:
    first_line = section.partition("\n")[0]
    try:
        fields = shlex.split(first_line)
    except ValueError:
        return None
    if len(fields) != 4 or fields[:2] != ["diff", "--git"]:
        return None
    return _decode_path(fields[3]) or _decode_path(fields[2])


def _untracked_patch(repository: Path, max_output_bytes: int) -> tuple[str, list[str]]:
    raw = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard", "-z"],
        cwd=repository,
        capture_output=True,
        check=False,
    )
    if raw.returncode != 0:
        raise GitError(raw.stderr.decode(errors="replace").strip() or "cannot list untracked files")
    paths = [item for item in raw.stdout.decode(errors="surrogateescape").split("\0") if item]
    sections: list[str] = []
    binary: list[str] = []
    for relative in paths:
        absolute = repository / relative
        mode = absolute.lstat().st_mode
        if not stat.S_ISREG(mode) and not stat.S_ISLNK(mode):
            binary.append(relative)
            continue
        if stat.S_ISREG(mode):
            with absolute.open("rb") as handle:
                if b"\0" in handle.read(8192):
                    binary.append(relative)
                    continue
        section = run_git(
            repository,
            "diff",
            "--no-index",
            "--no-ext-diff",
            "--no-textconv",
            "--",
            "/dev/null",
            relative,
            allow_diff=True,
            max_output_bytes=max_output_bytes
            - sum(len(item.encode("utf-8", errors="surrogateescape")) for item in sections),
        )
        sections.append(section)
    return "".join(sections), binary


def _matches(path: str, patterns: list[str]) -> bool:
    return any(fnmatch.fnmatch(path, pattern) or Path(path).match(pattern) for pattern in patterns)


def _is_binary_section(section: str) -> bool:
    for line in section.splitlines():
        if line.startswith("@@ "):
            return False
        if line == "GIT binary patch" or line.startswith("Binary files "):
            return True
    return False


def collect_diff(
    *,
    root: Path | None = None,
    base: str | None = None,
    committed_only: bool = False,
    staged_only: bool = False,
    excludes: list[str] | None = None,
    max_diff_bytes: int = 500_000,
) -> CollectedDiff:
    repository = repository_root(root)
    head_sha = _head_sha(repository)
    merge_base: str | None = None
    if staged_only and base:
        raise GitError("--staged-only cannot be combined with --base")
    if staged_only and committed_only:
        raise GitError("--staged-only cannot be combined with --committed-only")

    common = ["diff", "--find-renames", "--no-ext-diff", "--no-textconv"]
    if staged_only:
        mode = "staged"
        base_ref = "HEAD"
        base_sha = head_sha
        patch = run_git(
            repository, *common, "--cached", "HEAD", "--", max_output_bytes=max_diff_bytes
        )
    elif base:
        mode = "committed" if committed_only else "base"
        base_ref = base
        base_sha = run_git(repository, "rev-parse", "--verify", f"{base}^{{commit}}").strip()
        merge_base = run_git(repository, "merge-base", "HEAD", base).strip()
        if not merge_base:
            raise GitError(f"HEAD and {base!r} do not have a merge base")
        end = "HEAD" if committed_only else None
        arguments = [*common, merge_base]
        if end:
            arguments.append(end)
        arguments.append("--")
        patch = run_git(repository, *arguments, max_output_bytes=max_diff_bytes)
    elif committed_only:
        raise GitError("--committed-only requires --base")
    else:
        mode = "working-tree"
        base_ref = "HEAD"
        base_sha = head_sha
        patch = run_git(repository, *common, "HEAD", "--", max_output_bytes=max_diff_bytes)

    binary_files: list[str] = []
    if not staged_only and not committed_only:
        remaining = max_diff_bytes - len(patch.encode("utf-8", errors="surrogateescape"))
        if remaining <= 0:
            raise CoverageError(
                f"Git patch exceeded the configured {max_diff_bytes}-byte safety limit"
            )
        untracked, untracked_binary = _untracked_patch(repository, remaining)
        patch += untracked
        binary_files.extend(untracked_binary)

    patterns = excludes or []
    kept: list[str] = []
    reviewed: list[str] = []
    excluded: list[str] = []
    sections = split_file_patches(patch)
    if patch and not sections:
        raise CoverageError("Git returned a non-empty patch that could not be parsed safely")
    for section in sections:
        path = patch_path(section) or diff_header_path(section)
        if not path:
            raise CoverageError("could not resolve a changed file path from the Git patch")
        safe_path = path.encode("utf-8", errors="surrogateescape").decode(
            "utf-8", errors="backslashreplace"
        )
        if any(0xDC80 <= ord(character) <= 0xDCFF for character in section):
            binary_files.append(safe_path)
            continue
        if _matches(path, patterns):
            excluded.append(safe_path)
            continue
        if _is_binary_section(section):
            binary_files.append(safe_path)
            continue
        kept.append(section)
        reviewed.append(safe_path)
    effective = "".join(kept)
    size = len(effective.encode("utf-8", errors="surrogateescape"))
    if size > max_diff_bytes:
        raise CoverageError(
            f"review patch is {size} bytes, above the configured {max_diff_bytes}-byte safety limit"
        )
    return CollectedDiff(
        repository=repository,
        patch=effective,
        mode=mode,
        base_ref=base_ref,
        base_sha=base_sha,
        merge_base=merge_base,
        head_sha=head_sha,
        reviewed_files=sorted(set(reviewed)),
        excluded_files=sorted(set(excluded)),
        binary_files=sorted(set(binary_files)),
    )


def chunk_patch(patch: str, limit: int) -> list[str]:
    if len(patch.encode()) <= limit:
        return [patch]
    chunks: list[str] = []
    current = ""
    for section in split_file_patches(patch):
        if current and len((current + section).encode()) > limit:
            chunks.append(current)
            current = ""
        if len(section.encode()) <= limit:
            current += section
            continue
        hunk_at = section.find("@@ ")
        if hunk_at < 0:
            raise CoverageError("a single changed file exceeds the provider chunk limit")
        header = section[:hunk_at]
        hunks = re.split(r"(?m)(?=^@@ )", section[hunk_at:])
        for hunk in hunks:
            candidate = header + hunk
            if len(candidate.encode()) > limit:
                raise CoverageError("a single diff hunk exceeds the provider chunk limit")
            if current and len((current + candidate).encode()) > limit:
                chunks.append(current)
                current = ""
            current += candidate
    if current:
        chunks.append(current)
    if "".join(split_file_patches(patch)) == "" and patch:
        raise CoverageError("could not split the patch safely")
    return chunks
