#!/usr/bin/env python3
"""Expand/contract linter for SQL migrations (PSG-50, FR-50.7, BR-50.7, OPS-1707).

BR-50.7: a migration that drops a column cannot ship in the same release as the code that stops
using it. During a rolling deploy the previous release (N-1) keeps running against the migrated
schema, so a drop is safe only when neither this release's code nor N-1's code reads the name.
The drop belongs in a later release than the one that stopped using it.

Usage:
    python3 expand_contract_lint.py --migrations <dir of this release's new migrations> \
        --code services/org --previous-code /checkout/of/previous-tag/services/org

``--migrations`` takes the migrations added in this release (the caller passes the new files
only). ``--code`` (this release) and ``--previous-code`` (N-1, e.g. a checkout of the previous
release tag; an empty directory for the first release) are scanned for identifiers and are
repeatable. A dropped or renamed column or table whose name appears as a whole word in either
scan is a violation. Files inside a ``migrations`` directory are not scanned: earlier migrations
always name the column they created.

Scope and limits (stated so nobody over-trusts the linter):
  * Understands ``ALTER TABLE ... DROP [COLUMN] [IF EXISTS] name`` (several actions per ALTER),
    ``ALTER TABLE ... RENAME [COLUMN] old TO new`` and ``ALTER TABLE old RENAME TO new`` (a
    rename breaks N-1 the same way a drop does) and ``DROP TABLE|VIEW [IF EXISTS] a, b``.
    ``DROP CONSTRAINT``, index, function and type drops are not data N-1 reads and pass.
  * Name matching is by whole word, ignoring case, so a column named ``amount`` is also found in
    code that uses ``amount`` for an unrelated variable. That false positive blocks the release;
    the fix is a rename or a split release, never a silent skip.
  * SQL comments and string literals are stripped before parsing; quoted identifiers are unquoted.
  * ``DROP SCHEMA``, ``DROP DATABASE``, ``DROP OWNED`` and any ALTER TABLE drop or rename it
    cannot read are reported as unparsed violations, so they are seen, not missed.

Exit status: 0 no violations, 1 violations, 2 usage or I/O error. Stdlib only, Python 3.11+.
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path

CODE_SUFFIXES = {".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".sql", ".yaml", ".yml", ".json"}

IDENT = r'(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_$]*)'
QNAME = rf"{IDENT}(?:\s*\.\s*{IDENT})*"
ALTER_TABLE_RE = re.compile(
    rf"^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?({QNAME})\s*\*?\s+(.*)$", re.IGNORECASE | re.DOTALL
)
DROP_RELATION_RE = re.compile(
    r"^DROP\s+(TABLE|VIEW|MATERIALIZED\s+VIEW)\s+(?:IF\s+EXISTS\s+)?(.*?)(?:\s+(?:CASCADE|RESTRICT))?$",
    re.IGNORECASE | re.DOTALL,
)
# Whole-database drops the linter cannot reason about.
UNPARSED_DROP_RE = re.compile(r"^DROP\s+(?:SCHEMA|DATABASE|OWNED)\b", re.IGNORECASE)
DROP_ACTION_RE = re.compile(rf"^DROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?({IDENT})(?:\s+(?:CASCADE|RESTRICT))?$", re.IGNORECASE)
RENAME_COLUMN_RE = re.compile(rf"^RENAME\s+(?:COLUMN\s+)?({IDENT})\s+TO\s+{IDENT}$", re.IGNORECASE)
RENAME_TABLE_RE = re.compile(rf"^RENAME\s+TO\s+{IDENT}$", re.IGNORECASE)
HARMLESS_ACTION_RE = re.compile(r"^(?:DROP|RENAME)\s+CONSTRAINT\b", re.IGNORECASE)


def strip_sql(sql: str) -> str:
    """Remove comments and the contents of string literals, keeping quoted identifiers."""
    sql = re.sub(r"/\*.*?\*/", " ", sql, flags=re.DOTALL)
    sql = re.sub(r"--[^\n]*", " ", sql)
    return re.sub(r"'(?:[^']|'')*'", "''", sql)


def unquote(ident: str) -> str:
    """Postgres folds unquoted names to lower case; matching ignores case anyway."""
    ident = ident.strip()
    return ident[1:-1] if ident.startswith('"') else ident.lower()


def last_part(qname: str) -> str:
    return unquote(re.findall(IDENT, qname)[-1])


def split_top_level(text: str) -> list[str]:
    """Split on commas outside parentheses and double quotes."""
    parts, depth, quoted, cur = [], 0, False, []
    for ch in text:
        if ch == '"':
            quoted = not quoted
        elif not quoted and ch == "(":
            depth += 1
        elif not quoted and ch == ")":
            depth -= 1
        elif not quoted and depth == 0 and ch == ",":
            parts.append("".join(cur))
            cur = []
            continue
        cur.append(ch)
    parts.append("".join(cur))
    return [p.strip() for p in parts if p.strip()]


def dropped_names(sql: str) -> tuple[list[tuple[str, str]], int]:
    """Return ([(kind, name)], unparsed_count) for the contract actions in one migration.

    kind is "column", "table" or "view"; a rename reports the old name.
    """
    found: list[tuple[str, str]] = []
    unparsed = 0
    for stmt in strip_sql(sql).split(";"):
        stmt = " ".join(stmt.split())
        if not stmt:
            continue
        alter = ALTER_TABLE_RE.match(stmt)
        if alter:
            table = last_part(alter.group(1))
            for action in split_top_level(alter.group(2)):
                if HARMLESS_ACTION_RE.match(action):
                    continue
                if m := DROP_ACTION_RE.match(action):
                    found.append(("column", unquote(m.group(1))))
                elif m := RENAME_COLUMN_RE.match(action):
                    found.append(("column", unquote(m.group(1))))
                elif RENAME_TABLE_RE.match(action):
                    found.append(("table", table))
                elif re.match(r"^(?:DROP|RENAME)\b", action, re.IGNORECASE):
                    unparsed += 1
            continue
        if m := DROP_RELATION_RE.match(stmt):
            kind = "table" if m.group(1).upper() == "TABLE" else "view"
            names = split_top_level(m.group(2))
            if not names or not all(re.fullmatch(QNAME, n) for n in names):
                unparsed += 1
                continue
            found.extend((kind, last_part(n)) for n in names)
        elif UNPARSED_DROP_RE.match(stmt):
            unparsed += 1
    return found, unparsed


@dataclass(frozen=True)
class Violation:
    file: str
    kind: str  # "column" | "table" | "view" | "unparsed"
    name: str
    referenced_in: tuple[str, ...]
    release: str = "this release"  # or "the previous release (N-1)"

    def message(self) -> str:
        if self.kind == "unparsed":
            return f"OPS-1707 {self.file}: DROP or RENAME statement could not be parsed; split the release by hand"
        refs = ", ".join(self.referenced_in)
        return (
            f"OPS-1707 {self.file}: migration drops {self.kind} {self.name} still referenced in "
            f"{self.release} ({refs}); split releases"
        )


def code_identifiers(roots: list[Path]) -> dict[str, set[str]]:
    """Map each lower-cased word that appears in code to the files it appears in."""
    index: dict[str, set[str]] = {}
    for root in roots:
        files = [root] if root.is_file() else sorted(p for p in root.rglob("*") if p.is_file())
        for path in files:
            if path.suffix not in CODE_SUFFIXES or "migrations" in path.parent.parts:
                continue
            try:
                text = path.read_text(encoding="utf-8")
            except UnicodeDecodeError:
                continue
            for word in {w.lower() for w in re.findall(r"[A-Za-z_][A-Za-z0-9_]*", text)}:
                index.setdefault(word, set()).add(str(path))
    return index


def lint(
    migrations: dict[str, str],
    code_index: dict[str, set[str]],
    previous_index: dict[str, set[str]] | None = None,
) -> list[Violation]:
    """Pure check: migrations maps file name -> SQL text; the indexes map word -> files."""
    violations: list[Violation] = []
    releases = [("this release", code_index), ("the previous release (N-1)", previous_index or {})]
    for name in sorted(migrations):
        drops, unparsed = dropped_names(migrations[name])
        for _ in range(unparsed):
            violations.append(Violation(name, "unparsed", "", ()))
        for kind, ident in drops:
            for release, index in releases:
                refs = tuple(sorted(index.get(ident.lower(), set())))
                if refs:
                    violations.append(Violation(name, kind, ident, refs, release))
                    break
    return violations


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--migrations", type=Path, required=True, help="directory of this release's new migrations")
    parser.add_argument(
        "--code", type=Path, action="append", required=True, help="this release's code to scan (repeatable)"
    )
    parser.add_argument(
        "--previous-code",
        type=Path,
        action="append",
        required=True,
        help="the previous release's (N-1) code to scan (repeatable; an empty directory for the first release)",
    )
    args = parser.parse_args(argv)

    if not args.migrations.is_dir():
        print(f"error: {args.migrations} is not a directory", file=sys.stderr)
        return 2
    for root in args.code + args.previous_code:
        if not root.exists():
            print(f"error: {root} does not exist", file=sys.stderr)
            return 2

    try:
        migrations = {p.name: p.read_text(encoding="utf-8") for p in sorted(args.migrations.glob("*.sql"))}
    except (OSError, UnicodeDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    violations = lint(migrations, code_identifiers(args.code), code_identifiers(args.previous_code))
    for v in violations:
        print(v.message())
    if violations:
        print(f"{len(violations)} expand/contract violation(s)")
        return 1
    print(f"expand/contract clean: {len(migrations)} migration(s) checked")
    return 0


if __name__ == "__main__":
    sys.exit(main())
