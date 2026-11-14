#!/usr/bin/env python3
"""Remove Codex rollout files whose session IDs no longer exist in Nexbox.

By default this script deletes matching files. Pass --dry-run to inspect the
candidate list first. It only considers rollout-*.jsonl files under the
configured Codex sessions directory and reads the Nexbox database read-only.
"""

from __future__ import annotations

import argparse
import os
import re
import sqlite3
import sys
from pathlib import Path


DEFAULT_DB = Path("/home/ubuntu/nex/nexbox.db")
DEFAULT_SESSIONS_DIR = Path("/home/ubuntu/.codex/sessions/2026")
ROLLOUT_NAME = re.compile(
    r"^rollout-\d{4}-\d{2}-\d{2}T[^/]*-"
    r"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$",
    re.IGNORECASE,
)


def load_nexbox_session_ids(db_path: Path) -> set[str]:
    if not db_path.is_file():
        raise FileNotFoundError(f"Nexbox database not found: {db_path}")

    # URI quoting prevents special path characters from changing SQLite URI
    # parameters. mode=ro guarantees that this script cannot alter the DB.
    db_uri = db_path.resolve().as_uri() + "?mode=ro"
    conn = sqlite3.connect(db_uri, uri=True)
    try:
        tables = {
            row[0]
            for row in conn.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table'"
            )
        }
        if "sessions" not in tables:
            raise RuntimeError("Database has no sessions table; refusing to prune")
        columns = {
            row[1] for row in conn.execute("PRAGMA table_info(sessions)")
        }
        if "agent_session_id" not in columns:
            raise RuntimeError(
                "sessions table has no agent_session_id column; refusing to prune"
            )
        return {
            value
            for (value,) in conn.execute(
                "SELECT agent_session_id FROM sessions "
                "WHERE agent_session_id IS NOT NULL AND agent_session_id <> ''"
            )
        }
    finally:
        conn.close()


def find_orphans(sessions_dir: Path, known_ids: set[str]) -> list[Path]:
    if not sessions_dir.is_dir():
        raise NotADirectoryError(f"Codex sessions directory not found: {sessions_dir}")

    root = sessions_dir.resolve(strict=True)
    candidates: list[Path] = []
    for current, dirs, filenames in os.walk(root, followlinks=False):
        # Do not traverse symlinked directories.
        dirs[:] = [name for name in dirs if not (Path(current) / name).is_symlink()]
        for filename in filenames:
            match = ROLLOUT_NAME.fullmatch(filename)
            if not match or match.group(1) in known_ids:
                continue
            path = Path(current) / filename
            # Only regular files inside the requested root are eligible.
            if path.is_symlink() or not path.is_file():
                continue
            resolved = path.resolve(strict=True)
            if resolved.is_relative_to(root):
                candidates.append(resolved)
    return sorted(candidates)


def main() -> int:
    parser = argparse.ArgumentParser(
        description=(
            "Delete Codex rollout files whose UUID is absent from all "
            "Nexbox sessions.agent_session_id values."
        )
    )
    parser.add_argument("--db", type=Path, default=DEFAULT_DB, help="Nexbox SQLite database")
    parser.add_argument(
        "--sessions-dir",
        type=Path,
        default=DEFAULT_SESSIONS_DIR,
        help="Codex sessions directory to scan",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="list orphan files without deleting them",
    )
    args = parser.parse_args()

    try:
        known_ids = load_nexbox_session_ids(args.db)
        orphans = find_orphans(args.sessions_dir, known_ids)
    except (OSError, sqlite3.Error, RuntimeError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 2

    total_bytes = sum(path.stat().st_size for path in orphans)
    action = "Would remove" if args.dry_run else "Removing"
    print(f"{action} {len(orphans)} orphan rollout file(s), {total_bytes:,} bytes")
    for path in orphans:
        print(f"{path} ({path.stat().st_size:,} bytes)")

    if args.dry_run:
        print("Dry run only; no files changed.")
        return 0

    removed = 0
    errors = 0
    for path in orphans:
        try:
            # Recheck immediately before unlinking to avoid following a path
            # that was replaced after the scan.
            if path.is_symlink() or not path.is_file():
                print(f"Skipped changed/non-regular path: {path}", file=sys.stderr)
                errors += 1
                continue
            path.unlink()
            removed += 1
        except OSError as exc:
            print(f"Could not remove {path}: {exc}", file=sys.stderr)
            errors += 1

    print(f"Removed {removed} file(s); {errors} error(s).")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
