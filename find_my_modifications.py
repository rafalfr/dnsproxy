#!/usr/bin/env python3
"""

Scans all Go source files (.go) in the current directory (recursively)
and finds all modifications marked with 'rafal'.

Supported patterns:
  1. Single-line:  any line containing 'rafal' (e.g.  var x int // rafal code)
  2. Block:        lines between a line containing '// rafal' (opening)
                   and a line containing '// end rafal' (closing, inclusive)

Output is written to 'rafal_modifications.txt' in the current directory.
"""

import os
import re
import sys
from pathlib import Path


# ── pattern helpers ────────────────────────────────────────────────────────────

BLOCK_START  = re.compile(r'\s*rafal\b', re.IGNORECASE)   # // rafal …
BLOCK_END    = re.compile(r'\s*end\s+rafal\b', re.IGNORECASE)  # // end rafal …
INLINE_RAFAL = re.compile(r'\drafal\b', re.IGNORECASE)       # rafal anywhere on the line


def is_block_start(line: str) -> bool:
    """A line that opens a block: starts with (optional whitespace +) // rafal
    but is NOT a closing marker."""
    stripped = line.strip()
    return bool(BLOCK_START.search(stripped)) and not bool(BLOCK_END.search(stripped))


def is_block_end(line: str) -> bool:
    return bool(BLOCK_END.search(line))


def has_inline_rafal(line: str) -> bool:
    """Any line that mentions 'rafal' (covers both inline tags and block markers)."""
    return bool(INLINE_RAFAL.search(line))


# ── per-file scanner ───────────────────────────────────────────────────────────

def scan_file(filepath: Path):
    """
    Returns a list of Modification objects:
      { 'start_line': int, 'end_line': int, 'lines': [(lineno, text), ...], 'kind': str }
    """
    modifications = []

    try:
        text = filepath.read_text(encoding='utf-8', errors='replace')
    except OSError as exc:
        print(f"  [warning] cannot read {filepath}: {exc}", file=sys.stderr)
        return modifications

    all_lines = text.splitlines()

    in_block   = False
    block_start_line = None
    block_lines = []

    i = 0
    while i < len(all_lines):
        lineno = i + 1          # 1-based
        line   = all_lines[i]

        if not in_block:
            if is_block_start(line):
                # Could be a pure block opener (no real code on the same line)
                # OR an inline tag that also happens to look like // rafal.
                # We treat it as a block opener; if the very next non-empty
                # line does NOT eventually close, it degrades gracefully.
                in_block = True
                block_start_line = lineno
                block_lines = [(lineno, line)]
            elif has_inline_rafal(line):
                # Pure single-line modification
                modifications.append({
                    'kind':       'inline',
                    'start_line': lineno,
                    'end_line':   lineno,
                    'lines':      [(lineno, line)],
                })
        else:
            block_lines.append((lineno, line))
            if is_block_end(line):
                modifications.append({
                    'kind':       'block',
                    'start_line': block_start_line,
                    'end_line':   lineno,
                    'lines':      list(block_lines),
                })
                in_block    = False
                block_lines = []

        i += 1

    # Unclosed block — report what we have
    if in_block and block_lines:
        modifications.append({
            'kind':       'block (unclosed)',
            'start_line': block_start_line,
            'end_line':   block_lines[-1][0],
            'lines':      list(block_lines),
        })

    return modifications


# ── main ───────────────────────────────────────────────────────────────────────

def main():
    root = Path('.')
    go_files = sorted(root.rglob('*.go'))

    if not go_files:
        print("No .go files found in the current directory tree.")
        sys.exit(0)

    output_path = Path('rafal_modifications.txt')
    total_mods  = 0

    with output_path.open('w', encoding='utf-8') as out:
        out.write("=" * 78 + "\n")
        out.write("RAFAL MODIFICATION REPORT\n")
        out.write(f"Scanned root : {root.resolve()}\n")
        out.write(f"Go files found: {len(go_files)}\n")
        out.write("=" * 78 + "\n\n")

        for go_file in go_files:
            mods = scan_file(go_file)
            if not mods:
                continue

            total_mods += len(mods)
            rel_path = go_file.as_posix()

            out.write("─" * 78 + "\n")
            out.write(f"FILE: {rel_path}  ({len(mods)} modification(s))\n")
            out.write("─" * 78 + "\n\n")

            for idx, mod in enumerate(mods, 1):
                kind = mod['kind']
                sl   = mod['start_line']
                el   = mod['end_line']

                if sl == el:
                    header = f"  [{idx}] {kind.upper()}  —  line {sl}"
                else:
                    header = f"  [{idx}] {kind.upper()}  —  lines {sl}–{el}"

                out.write(header + "\n")
                out.write("  " + "·" * (len(header) - 2) + "\n")

                #for lineno, text in mod['lines']:
                #    out.write(f"  {lineno:>6} │ {text}\n")

                out.write("\n")

        out.write("=" * 78 + "\n")
        out.write(f"TOTAL modifications found: {total_mods}\n")
        out.write("=" * 78 + "\n")

    print(f"Done. {total_mods} modification(s) found across {len(go_files)} Go file(s).")
    print(f"Report written to: {output_path.resolve()}")


if __name__ == '__main__':
    main()
