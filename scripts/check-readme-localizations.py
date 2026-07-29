#!/usr/bin/env python3
"""Check machine-verifiable contracts of localized README files.

Localization is not a byte-for-byte translation exercise. This checker preserves
what automation can prove: document shape, executable examples, and external
links. Native-language review remains responsible for prose and meaning.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

CANONICAL = "README.md"
LOCALIZED = ("README.ko.md", "README.ja.md", "README.zh-CN.md")
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.MULTILINE | re.DOTALL)
HEADING = re.compile(r"^(#{1,6})\s+", re.MULTILINE)
EXTERNAL_LINK = re.compile(r"\]\((https?://[^)]+)\)")


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError as err:
        raise ValueError(f"cannot read {path.name}: {err}") from err


def executable(block: str) -> str:
    return "\n".join(
        line for line in block.splitlines() if not line.lstrip().startswith("#")
    ).strip()


def contract(text: str) -> tuple[list[int], list[str], list[str]]:
    headings = [len(match.group(1)) for match in HEADING.finditer(text)]
    code = [executable(block) for block in FENCE.findall(text)]
    links = sorted(EXTERNAL_LINK.findall(text))
    return headings, code, links


def main() -> int:
    root = Path(sys.argv[1]).resolve() if len(sys.argv) == 2 else Path.cwd()
    if len(sys.argv) > 2:
        print(f"usage: {Path(sys.argv[0]).name} [repository-root]", file=sys.stderr)
        return 2

    try:
        canonical = contract(read(root / CANONICAL))
    except ValueError as err:
        print(f"README localization check: {err}", file=sys.stderr)
        return 1

    failed = False
    for name in LOCALIZED:
        try:
            localized = contract(read(root / name))
        except ValueError as err:
            print(f"README localization check: {err}", file=sys.stderr)
            failed = True
            continue
        for label, expected, actual in zip(
            ("heading levels", "executable code blocks", "external links"),
            canonical,
            localized,
        ):
            if actual != expected:
                print(
                    f"README localization check: {name} {label} differ from {CANONICAL}",
                    file=sys.stderr,
                )
                failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
