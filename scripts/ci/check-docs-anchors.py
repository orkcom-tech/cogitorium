#!/usr/bin/env python3
"""Every in-page link in the documentation sidebar must reach a real heading.

This exists because twelve of them did not, and nothing said so. A dead anchor
does not 404 and does not warn — the page simply fails to scroll, which nobody
notices while writing and everybody notices while reading. The cause is that
GitHub Pages builds these with kramdown, and kramdown STRIPS a leading number
from a heading when it makes the id: "## 4. Rules an agent must not break"
becomes "rules-an-agent-must-not-break", not "4-rules-...". A numbered
walkthrough is exactly the shape that gets this wrong.

Run: python3 scripts/ci/check-docs-anchors.py
Exits non-zero, naming each broken link and the ids that do exist.
"""

import re
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parents[2] / "docs"


def kramdown_id(text: str) -> str:
    """kramdown's generate_id, transcribed from its source.

    Leading non-letters are dropped, anything outside [a-zA-Z0-9 -] is removed,
    then EACH space becomes a hyphen — which is why an em dash surrounded by
    spaces yields a double hyphen rather than a single one.
    """
    s = re.sub(r"^[^a-zA-Z]+", "", text)
    s = re.sub(r"[^a-zA-Z0-9 -]", "", s)
    return s.replace(" ", "-").lower()


def heading_ids(path: Path) -> dict[str, str]:
    ids: dict[str, str] = {}
    fenced = False
    for line in path.read_text().splitlines():
        # A "## " inside a fenced block is sample output, not a heading. The
        # guide quotes the prompt section verbatim, so this matters.
        if line.startswith("```"):
            fenced = not fenced
            continue
        if fenced:
            continue
        m = re.match(r"^(#{1,6})\s+(.*?)\s*$", line)
        if m:
            ids[kramdown_id(m.group(2))] = m.group(2)
    return ids


def main() -> int:
    layout = (DOCS / "_layouts" / "default.html").read_text()
    # The sidebar renders one anchor list per page, chosen by a Liquid
    # conditional; each branch is checked against the page it belongs to.
    branches = re.search(
        r"\{%-?\s*if on_guide\s*-?%\}(.*?)\{%-?\s*else\s*-?%\}(.*?)\{%-?\s*endif", layout, re.S
    )
    if not branches:
        print("the sidebar no longer has the two-page branch this check knows about", file=sys.stderr)
        return 1

    failures = 0
    for label, page, block in (
        ("guide.md", DOCS / "guide.md", branches.group(1)),
        ("index.md", DOCS / "index.md", branches.group(2)),
    ):
        ids = heading_ids(page)
        links = re.findall(r'href="#([^"]+)"', block)
        broken = [l for l in links if l not in ids]
        print(f"{label}: {len(links)} sidebar links, {len(broken)} broken")
        for b in broken:
            print(f"  no heading has the id {b!r}", file=sys.stderr)
        if broken:
            print(f"  ids that exist in {label}: {sorted(ids)}", file=sys.stderr)
        failures += len(broken)

    if failures:
        print(f"\n{failures} sidebar link(s) point at nothing", file=sys.stderr)
        return 1
    print("ok: every sidebar link reaches a heading")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
