#!/usr/bin/env python3
"""Report remaining CJK characters in tracked NexBoot text files.

The intentionally Chinese README.zh-CN.md is excluded. Files containing NUL
bytes and known binary assets are skipped. Exit status 1 means work remains.
"""

from __future__ import annotations

import argparse
from collections import defaultdict
from pathlib import Path
import re
import subprocess
import sys

CJK = re.compile(r"[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]")
BINARY_EXTENSIONS = {
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svgz", ".ico",
    ".efi", ".kpxe", ".woff", ".woff2", ".ttf", ".zip", ".gz",
    ".pdf", ".deb", ".raw", ".exe", ".dll", ".bin",
}
EXCLUDED_NAMES = {"readme.zh-cn.md"}


def tracked_files(root: Path):
    found = subprocess.run(
        ["git", "ls-files", "-z"], cwd=root, check=True,
        stdout=subprocess.PIPE,
    )
    for name in found.stdout.decode("utf-8", errors="replace").split("\0"):
        if not name:
            continue
        path = Path(name)
        if path.name.lower() in EXCLUDED_NAMES:
            continue
        if path.suffix.lower() in BINARY_EXTENSIONS:
            continue
        yield name, root / path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--max-lines", type=int, default=60,
                        help="Maximum matching lines to print (default: 60)")
    parser.add_argument("--frontend-tests-only", action="store_true",
                        help="Check only web frontend test files")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    affected = defaultdict(list)
    checked = 0

    for relative, path in tracked_files(root):
        if args.frontend_tests_only and not (
            relative.startswith("web/") and relative.endswith(".test.jsx")
        ):
            continue
        try:
            raw = path.read_bytes()
        except (OSError, PermissionError):
            continue
        if b"\0" in raw:
            continue
        try:
            content = raw.decode("utf-8")
        except UnicodeDecodeError:
            continue
        checked += 1
        for index, line in enumerate(content.splitlines(), 1):
            if CJK.search(line):
                affected[relative].append((index, line.strip()))

    count = 0
    for filename, matches in sorted(
        affected.items(), key=lambda item: (-len(item[1]), item[0])
    ):
        print(f"{filename}: {len(matches)} lines")
        for number, line in matches:
            if count >= args.max_lines:
                continue
            print(f"  {number}: {line[:180]}")
            count += 1

    total = sum(len(lines) for lines in affected.values())
    print(f"\nChecked {checked} UTF-8 files; "
          f"{len(affected)} contain CJK text across {total} lines.")
    print("Excluded README.zh-CN.md (case-insensitive).")
    return 1 if affected else 0


if __name__ == "__main__":
    sys.exit(main())
