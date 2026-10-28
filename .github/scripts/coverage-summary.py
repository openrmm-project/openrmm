#!/usr/bin/env python3
"""Per-package statement coverage from a Go cover profile, as a Markdown table.

    go test ./... -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
    python3 .github/scripts/coverage-summary.py coverage.out >> "$GITHUB_STEP_SUMMARY"

With -coverpkg every test binary reports every package, so the same block
appears once per binary: a block counts as covered if any binary ran it.
"""
import sys
from collections import defaultdict

MODULE = 'github.com/p1ngu/rmm/'


def main(path):
    blocks = {}  # "file:range" -> (statements, covered)
    with open(path, encoding='utf-8') as profile:
        for line in profile:
            line = line.strip()
            if not line or line.startswith('mode:'):
                continue
            where, statements, count = line.rsplit(' ', 2)
            seen = blocks.get(where)
            covered = int(count) > 0 or (seen is not None and seen[1])
            blocks[where] = (int(statements), covered)

    packages = defaultdict(lambda: [0, 0])  # package -> [statements, covered]
    for where, (statements, covered) in blocks.items():
        package = where.split(':', 1)[0].rsplit('/', 1)[0].removeprefix(MODULE)
        packages[package][0] += statements
        packages[package][1] += statements if covered else 0

    total = [sum(p[0] for p in packages.values()), sum(p[1] for p in packages.values())]
    pct = lambda s, c: f'{100 * c / s:.1f}%' if s else '—'
    print('### Go coverage (unit and database tests, across packages)\n')
    print(f'**Total: {pct(*total)}** of {total[0]} statements\n')
    print('| package | statements | covered |')
    print('|---|---:|---:|')
    for package in sorted(packages):
        statements, covered = packages[package]
        print(f'| `{package}` | {statements} | {pct(statements, covered)} |')


if __name__ == '__main__':
    main(sys.argv[1])
