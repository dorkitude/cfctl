#!/usr/bin/env python3
"""Regenerate the summary and per-command tables in docs/cfctl-vs-wrangler.md
from docs/wrangler-map/*.tsv (the source of truth).

Usage: scripts/gen-wrangler-doc.py [--check]   (run from the repo root, or `make wrangler-doc`)

Each TSV has the columns wrangler, cfctl, status, notes. A header row is
optional (any row whose status column is "status" is skipped). The wrangler
column may or may not start with "wrangler ". Status is full, partial, or
not-applicable. With --check, exits 1 if the doc is out of date instead of
writing it.
"""
import collections
import csv
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOC = os.path.join(ROOT, "docs", "cfctl-vs-wrangler.md")
AREAS = [("Workers", "workers"), ("Storage", "storage"), ("Platform", "platform")]
ICONS = {"full": "✅ full", "partial": "🟡 partial", "not-applicable": "➖ n/a"}


def read_rows(name):
    rows = []
    with open(os.path.join(ROOT, "docs", "wrangler-map", name + ".tsv"), newline="") as f:
        for n, r in enumerate(csv.reader(f, delimiter="\t", quoting=csv.QUOTE_NONE), 1):
            if not r or not "".join(r).strip():
                continue
            if len(r) < 3:
                sys.exit(f"{name}.tsv:{n}: expected at least 3 tab-separated columns")
            if r[2].strip().lower() == "status":
                continue  # header row
            st = r[2].strip()
            if st not in ICONS:
                sys.exit(f"{name}.tsv:{n}: unknown status {st!r} (want full, partial, not-applicable)")
            w = r[0].strip()
            if not w.startswith("wrangler"):
                w = "wrangler " + w
            rows.append((w, r[1].strip(), st, (r[3] if len(r) > 3 else "").strip()))
    return rows


def generate():
    total = collections.Counter()
    summary = ["| Area | wrangler commands | ✅ full | 🟡 partial | ➖ n/a |", "|---|---:|---:|---:|---:|"]
    tables = []
    for area, name in AREAS:
        rows = read_rows(name)
        c = collections.Counter(r[2] for r in rows)
        total.update(c)
        summary.append(f"| {area} | {len(rows)} | {c['full']} | {c['partial']} | {c['not-applicable']} |")
        tables += [
            f"### {area} ({len(rows)} commands: {c['full']} full, {c['partial']} partial, {c['not-applicable']} n/a)",
            "",
            f"Source: [`docs/wrangler-map/{name}.tsv`](wrangler-map/{name}.tsv)",
            "",
            "| wrangler | cfctl | status | notes |",
            "|---|---|---|---|",
        ]
        for w, cf, st, notes in rows:
            cell = "—" if cf in ("", "-", "—") else f"`{cf}`"
            tables.append(f"| `{w}` | {cell} | {ICONS[st]} | {notes.replace('|', chr(92) + '|')} |")
        tables.append("")
    n = sum(total.values())
    summary.append(f"| **Total** | **{n}** | **{total['full']}** | **{total['partial']}** | **{total['not-applicable']}** |")
    headline = (f"**{n} wrangler commands → {total['full']} full, {total['partial']} partial, "
                f"{total['not-applicable']} not applicable.**")
    return "\n".join(summary), "\n".join(tables).rstrip(), headline


def replace_block(text, tag, body):
    pat = re.compile(r"(<!-- BEGIN GENERATED %s[^>]*-->\n).*?(\n<!-- END GENERATED %s -->)" % (tag, tag), re.S)
    if not pat.search(text):
        sys.exit(f"{DOC}: missing BEGIN/END GENERATED {tag} markers")
    return pat.sub(lambda m: m.group(1) + body + m.group(2), text)


def main():
    summary, tables, headline = generate()
    old = open(DOC).read()
    new = replace_block(old, "SUMMARY", summary)
    new = replace_block(new, "TABLES", tables)
    new = re.sub(r"\*\*\d+ wrangler commands → \d+ full, \d+ partial, \d+ not\s+applicable\.\*\*", headline, new)
    if "--check" in sys.argv[1:]:
        if new != old:
            print(f"{os.path.relpath(DOC, ROOT)} is out of date; run make wrangler-doc", file=sys.stderr)
            sys.exit(1)
        return
    if new != old:
        open(DOC, "w").write(new)
        print(f"updated {os.path.relpath(DOC, ROOT)}")
    print(headline.strip("*"))


if __name__ == "__main__":
    main()
