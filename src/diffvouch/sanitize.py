from __future__ import annotations

import re

SECRET_PATTERNS = [
    re.compile(
        r"(?i)(?<![A-Za-z0-9_])"
        r"((?:[A-Za-z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|client[_-]?secret|"
        r"password|secret[_-]?access[_-]?key|secret[_-]?key|private[_-]?key))\b"
        r"(\s*[:=]\s*)(['\"])([^\r\n]*?)(\3)"
    ),
    re.compile(
        r"(?i)(?<![A-Za-z0-9_])"
        r"((?:[A-Za-z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|client[_-]?secret|"
        r"password|secret[_-]?access[_-]?key|secret[_-]?key|private[_-]?key))\b"
        r"(\s*[:=]\s*)()(?!\[REDACTED BY DIFFVOUCH\])([^\s,;#{}\[\]\r\n]{8,})()"
    ),
    re.compile(r"\b((?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,})\b"),
    re.compile(r"\b(sk-[A-Za-z0-9_-]{20,})\b"),
    re.compile(r"\b((?:AKIA|ASIA)[A-Z0-9]{16})\b"),
    re.compile(
        r"(?ms)^[ +\-]?-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----.*?"
        r"^[ +\-]?-----END (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----$"
    ),
]


def redact_secrets(value: str) -> tuple[str, int]:
    count = 0
    output = value
    for pattern in SECRET_PATTERNS:

        def replace(match: re.Match[str]) -> str:
            nonlocal count
            count += 1
            if match.lastindex and match.lastindex >= 5:
                quote = match.group(3)
                return f"{match.group(1)}{match.group(2)}{quote}[REDACTED BY DIFFVOUCH]{quote}"
            if "PRIVATE KEY-----" in match.group(0):
                redacted_lines: list[str] = []
                for line in match.group(0).splitlines(keepends=True):
                    ending = "\n" if line.endswith("\n") else ""
                    content = line[:-1] if ending else line
                    prefix = content[:1] if content[:1] in {" ", "+", "-"} else ""
                    redacted_lines.append(f"{prefix}[REDACTED BY DIFFVOUCH]{ending}")
                return "".join(redacted_lines)
            return "[REDACTED BY DIFFVOUCH]"

        output = pattern.sub(replace, output)
    return output, count
