#!/usr/bin/env python3
"""Decide whether a push or pull request touches any watched path.

Usage: ci-changed.py PATTERNS_FILE BASE HEAD [--merge-base]

Prints "true" or "false". Prints "true" when BASE is missing, all zeros, or not
in the checkout, so an unknown history runs the work instead of skipping it.
Patterns use GitHub's path-filter syntax: '*' stays inside one directory, '**'
crosses directories, and a leading '!' excludes. The last matching pattern
wins, as in GitHub's own filters.
"""

import re
import subprocess
import sys


def load_patterns(path):
    with open(path, encoding="utf-8") as patterns_file:
        lines = (line.strip() for line in patterns_file)
        return [line for line in lines if line and not line.startswith("#")]


def pattern_regex(pattern):
    escaped = re.escape(pattern)
    escaped = escaped.replace(r"\*\*/", "(?:.*/)?").replace(r"\*\*", ".*")
    return re.compile(escaped.replace(r"\*", "[^/]*"))


def matches(patterns, filename):
    included = False
    for pattern in patterns:
        exclude = pattern.startswith("!")
        if pattern_regex(pattern.removeprefix("!")).fullmatch(filename):
            included = not exclude
    return included


def changed_files(base, head, merge_base):
    revision_range = f"{base}...{head}" if merge_base else f"{base}..{head}"
    output = subprocess.check_output(
        ["git", "diff", "--name-only", "--no-renames", revision_range], text=True
    )
    return [line for line in output.splitlines() if line]


def known_commit(revision):
    if not revision or set(revision) == {"0"}:
        return False
    probe = subprocess.run(
        ["git", "cat-file", "-e", f"{revision}^{{commit}}"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return probe.returncode == 0


def main(arguments):
    merge_base = "--merge-base" in arguments
    positional = [argument for argument in arguments if argument != "--merge-base"]
    if len(positional) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    patterns_path, base, head = positional
    patterns = load_patterns(patterns_path)
    if not known_commit(base):
        print("true")
        return 0
    files = changed_files(base, head, merge_base)
    print("true" if any(matches(patterns, name) for name in files) else "false")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
