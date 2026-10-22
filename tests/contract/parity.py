#!/usr/bin/env python3
"""Compare Python and the built Go validator against every fixture expectation."""

import argparse
from pathlib import Path
import subprocess
import sys

from check import CASES, FIXTURES, inspect


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path, help='path to the built pulltide CLI')
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error('validator binary does not exist; build ./cmd/pulltide first')

    paths = [case['file'] for case in CASES]
    actual = {str(path.relative_to(FIXTURES)) for path in FIXTURES.glob('*/*.yaml')}
    if not paths or len(paths) != len(set(paths)) or set(paths) != actual:
        print('Fixture manifest must list every YAML fixture exactly once.', file=sys.stderr)
        return 1

    failures = 0
    valid_count = 0
    for case in CASES:
        if case['expect'] not in {'valid', 'yaml', 'schema', 'semantic'}:
            print(f"Unknown expected outcome: {case['file']}", file=sys.stderr)
            failures += 1
            continue
        path = FIXTURES / case['file']
        expected = case['expect'] == 'valid'
        valid_count += expected
        _, issues = inspect(path.read_text(encoding='utf-8'))
        python_valid = not issues
        try:
            result = subprocess.run(
                [str(binary), 'validate', str(path)],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                timeout=30, check=False,
            )
        except (OSError, subprocess.TimeoutExpired):
            print(f"Go validator could not complete: {case['file']}", file=sys.stderr)
            failures += 1
            continue
        go_valid = result.returncode == 0
        if result.returncode not in (0, 1) or python_valid != expected or go_valid != expected:
            print(
                f"Mismatch: {case['file']}: expected_valid={expected}, "
                f"python_valid={python_valid}, go_exit={result.returncode}",
                file=sys.stderr,
            )
            failures += 1

    print(f'Fixture parity: {len(CASES)} fixtures ({valid_count} valid, '
          f'{len(CASES) - valid_count} invalid); {failures} failures.')
    return int(failures != 0)


if __name__ == '__main__':
    sys.exit(main())
