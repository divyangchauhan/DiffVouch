#!/usr/bin/env python3
"""Publish one validated DiffVouch COMMENT review through GitHub CLI."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


class PublicationError(Exception):
    pass


def run_gh(*args: str, input_data: bytes | None = None) -> str:
    try:
        result = subprocess.run(
            ["gh", *args],
            input=input_data,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
    except FileNotFoundError as exc:
        raise PublicationError("GitHub CLI 'gh' is not installed") from exc
    if result.returncode != 0:
        detail = result.stderr.decode(errors="replace").strip()
        raise PublicationError(detail or f"gh {' '.join(args)} failed")
    return result.stdout.decode()


def load_payload(source: str) -> dict[str, Any]:
    try:
        raw = sys.stdin.read() if source == "-" else Path(source).read_text(encoding="utf-8")
        value = json.loads(raw)
    except (OSError, json.JSONDecodeError) as exc:
        raise PublicationError(f"cannot read publication payload: {exc}") from exc
    if not isinstance(value, dict):
        raise PublicationError("publication payload must be a JSON object")
    return value


def validate_payload(payload: dict[str, Any]) -> None:
    if payload.get("schemaVersion") != 1:
        raise PublicationError("publication payload schemaVersion must be 1")
    if payload.get("status") != "complete" or payload.get("partial") is not False:
        raise PublicationError("only complete, non-partial reviews may be published")
    commit = payload.get("reviewedCommit")
    if not isinstance(commit, str) or re.fullmatch(r"[0-9a-fA-F]{40}", commit) is None:
        raise PublicationError("reviewedCommit must be a full 40-character Git SHA")
    if not isinstance(payload.get("body"), str) or not payload["body"].strip():
        raise PublicationError("review body must be a non-empty string")
    comments = payload.get("comments", [])
    if not isinstance(comments, list):
        raise PublicationError("comments must be an array")
    for index, comment in enumerate(comments):
        if not isinstance(comment, dict):
            raise PublicationError(f"comment {index} must be an object")
        if not isinstance(comment.get("path"), str) or not comment["path"]:
            raise PublicationError(f"comment {index} has an invalid path")
        if not isinstance(comment.get("line"), int) or comment["line"] < 1:
            raise PublicationError(f"comment {index} has an invalid line")
        if comment.get("side") not in {"LEFT", "RIGHT"}:
            raise PublicationError(f"comment {index} side must be LEFT or RIGHT")
        if not isinstance(comment.get("body"), str) or not comment["body"].strip():
            raise PublicationError(f"comment {index} has an empty body")
        has_start_line = "start_line" in comment
        has_start_side = "start_side" in comment
        if has_start_line != has_start_side:
            raise PublicationError(f"comment {index} must supply start_line and start_side together")
        if has_start_line:
            if not isinstance(comment["start_line"], int) or comment["start_line"] < 1:
                raise PublicationError(f"comment {index} has an invalid start_line")
            if comment["start_side"] not in {"LEFT", "RIGHT"}:
                raise PublicationError(f"comment {index} start_side must be LEFT or RIGHT")


def decode_git_path(header_value: str) -> str | None:
    if header_value == "/dev/null":
        return None
    if not (header_value.startswith('"') and header_value.endswith('"')):
        value = header_value
    else:
        encoded = bytearray()
        value = header_value[1:-1]
        index = 0
        escapes = {
            "a": 7,
            "b": 8,
            "f": 12,
            "n": 10,
            "r": 13,
            "t": 9,
            "v": 11,
            "\\": 92,
            '"': 34,
        }
        while index < len(value):
            character = value[index]
            if character != "\\":
                encoded.extend(character.encode("utf-8"))
                index += 1
                continue
            index += 1
            if index >= len(value):
                raise PublicationError("invalid trailing escape in Git diff path")
            escaped = value[index]
            if escaped in escapes:
                encoded.append(escapes[escaped])
                index += 1
            elif escaped in "01234567":
                end = index + 1
                while end < min(index + 3, len(value)) and value[end] in "01234567":
                    end += 1
                encoded.append(int(value[index:end], 8))
                index = end
            else:
                encoded.extend(escaped.encode("utf-8"))
                index += 1
        value = encoded.decode("utf-8", errors="surrogateescape")
    if value.startswith(("a/", "b/")):
        return value[2:]
    return value


DiffLocation = tuple[str, str, int]
DiffPosition = tuple[int, int, int]


def parse_diff_positions(diff: str) -> tuple[set[DiffLocation], dict[DiffLocation, DiffPosition]]:
    changed: set[DiffLocation] = set()
    positions: dict[DiffLocation, DiffPosition] = {}
    old_path: str | None = None
    new_path: str | None = None
    in_hunk = False
    file_number = 0
    hunk_number = 0
    position_in_hunk = 0
    old_line = new_line = 0
    for raw_line in diff.splitlines():
        if raw_line.startswith("diff --git "):
            file_number += 1
            hunk_number = 0
            old_path = None
            new_path = None
            in_hunk = False
        elif not in_hunk and raw_line.startswith("--- "):
            old_path = decode_git_path(raw_line[4:])
        elif not in_hunk and raw_line.startswith("+++ "):
            new_path = decode_git_path(raw_line[4:])
        elif raw_line.startswith("@@ "):
            match = re.match(r"@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@", raw_line)
            if match:
                old_line, new_line = map(int, match.groups())
                in_hunk = True
                hunk_number += 1
                position_in_hunk = 0
        elif in_hunk and new_path is not None and raw_line.startswith("+"):
            position_in_hunk += 1
            location = (new_path, "RIGHT", new_line)
            changed.add(location)
            positions[location] = (file_number, hunk_number, position_in_hunk)
            new_line += 1
        elif in_hunk and (new_path or old_path) is not None and raw_line.startswith("-"):
            position_in_hunk += 1
            location = (new_path or old_path, "LEFT", old_line)
            changed.add(location)
            positions[location] = (file_number, hunk_number, position_in_hunk)
            old_line += 1
        elif in_hunk and (new_path or old_path) is not None and raw_line.startswith(" "):
            position_in_hunk += 1
            path = new_path or old_path
            positions[(path, "LEFT", old_line)] = (file_number, hunk_number, position_in_hunk)
            positions[(path, "RIGHT", new_line)] = (file_number, hunk_number, position_in_hunk)
            old_line += 1
            new_line += 1
    return changed, positions


def parse_changed_lines(diff: str) -> set[DiffLocation]:
    changed, _ = parse_diff_positions(diff)
    return changed


def validate_inline_locations(comments: list[dict[str, Any]], diff: str) -> None:
    changed_lines, positions = parse_diff_positions(diff)
    invalid: list[str] = []
    for comment in comments:
        end = (comment["path"], comment["side"], comment["line"])
        if end not in changed_lines:
            invalid.append(f"{comment['path']}:{comment['line']} ({comment['side']})")
            continue
        if "start_line" not in comment:
            continue
        start = (comment["path"], comment["start_side"], comment["start_line"])
        if comment["start_side"] != comment["side"]:
            invalid.append(
                f"{comment['path']}:{comment['start_line']}-{comment['line']} "
                f"({comment['start_side']}..{comment['side']}; range crosses diff sides)"
            )
            continue
        start_position = positions.get(start)
        end_position = positions[end]
        if (
            start_position is None
            or start_position[:2] != end_position[:2]
            or start_position[2] >= end_position[2]
        ):
            invalid.append(
                f"{comment['path']}:{comment['start_line']}-{comment['line']} "
                f"({comment['side']}; invalid live-diff range)"
            )
    if invalid:
        raise PublicationError(
            "inline locations are not valid changed lines or ranges in the live PR diff: "
            + ", ".join(invalid)
        )


def discover_repo(explicit: str | None) -> str:
    repo = explicit or run_gh("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner").strip()
    if re.fullmatch(r"[^/\s]+/[^/\s]+", repo) is None:
        raise PublicationError("could not resolve a GitHub repository as owner/name")
    return repo


def discover_pr(repo: str, explicit: int | None) -> int:
    if explicit is not None:
        return explicit
    raw = run_gh("pr", "view", "--repo", repo, "--json", "number,state", "--jq", ".number").strip()
    try:
        return int(raw)
    except ValueError as exc:
        raise PublicationError("could not resolve exactly one pull request for the current branch; pass --pr") from exc


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, help="payload JSON path, or - for stdin")
    parser.add_argument("--repo", help="GitHub owner/name override")
    parser.add_argument("--pr", type=int, help="pull request number override")
    parser.add_argument("--dry-run", action="store_true", help="validate and print the API body without GitHub access")
    args = parser.parse_args()

    try:
        payload = load_payload(args.input)
        validate_payload(payload)
        api_body = {
            "commit_id": payload["reviewedCommit"],
            "body": payload["body"],
            "event": "COMMENT",
            "comments": payload.get("comments", []),
        }
        if args.dry_run:
            print(json.dumps(api_body, indent=2, sort_keys=True))
            return 0

        run_gh("auth", "status", "--hostname", "github.com")
        repo = discover_repo(args.repo)
        pr = discover_pr(repo, args.pr)
        pr_data = json.loads(run_gh("api", f"repos/{repo}/pulls/{pr}"))
        if pr_data.get("state") != "open":
            raise PublicationError(f"pull request {repo}#{pr} is not open")
        live_head = pr_data.get("head", {}).get("sha")
        if live_head != payload["reviewedCommit"]:
            raise PublicationError(
                f"PR head changed: reviewed {payload['reviewedCommit']}, current {live_head}; run a fresh review"
            )

        diff = run_gh("api", "-H", "Accept: application/vnd.github.v3.diff", f"repos/{repo}/pulls/{pr}")
        validate_inline_locations(api_body["comments"], diff)

        response = json.loads(
            run_gh(
                "api",
                "--method",
                "POST",
                "-H",
                "Accept: application/vnd.github+json",
                f"repos/{repo}/pulls/{pr}/reviews",
                "--input",
                "-",
                input_data=json.dumps(api_body).encode(),
            )
        )
        result = {
            "published": True,
            "repo": repo,
            "pr": pr,
            "reviewId": response.get("id"),
            "url": response.get("html_url"),
            "inlineComments": len(api_body["comments"]),
        }
        print(json.dumps(result, sort_keys=True))
        return 0
    except PublicationError as exc:
        print(f"publication failed: {exc}", file=sys.stderr)
        return 5
    except (json.JSONDecodeError, KeyError, TypeError) as exc:
        print(f"publication failed: invalid GitHub response or payload: {exc}", file=sys.stderr)
        return 5


if __name__ == "__main__":
    raise SystemExit(main())
