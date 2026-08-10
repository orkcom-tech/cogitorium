#!/usr/bin/env python3
"""Every in-page link in the documentation sidebar must reach a real heading.

A dead anchor does not 404 and does not warn — the page simply fails to scroll,
which nobody notices while writing and everybody notices while reading. So it
gets checked here.

The slug rule below was NOT reasoned out. An earlier version of this script
transcribed an old kramdown, which strips a leading digit out of a heading id,
and on that evidence twelve working links were "fixed" into twelve dead ones.
The rule here is the one the published site actually produces, read back out of
its own HTML, and SELF_TEST pins the cases that settled it.

Run: python3 scripts/ci/check-docs-anchors.py
"""

import re
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parents[2] / "docs"

# Heading text -> the id GitHub Pages published for it, copied from the built
# page. Each entry is here because it distinguishes a candidate rule from a
# wrong one: the leading number is KEPT; an em dash is dropped and leaves the
# two spaces around it, which become two hyphens; a comma is dropped and leaves
# the single space beside it, which becomes one hyphen.
SELF_TEST = {
    "1. From nothing to an answer": "1-from-nothing-to-an-answer",
    "3. Gears — tools your agents keep": "3-gears--tools-your-agents-keep",
    "The token, and when you need it": "the-token-and-when-you-need-it",
    "10. More than one person": "10-more-than-one-person",
}


def slug(text: str) -> str:
    """The id the site gives a heading: drop anything outside [a-zA-Z0-9 -],
    turn EACH space into a hyphen, lowercase."""
    return re.sub(r"[^a-zA-Z0-9 -]", "", text).replace(" ", "-").lower()


def heading_ids(path: Path) -> dict[str, str]:
    ids: dict[str, str] = {}
    fenced = False
    for line in path.read_text().splitlines():
        # A "## " inside a fenced block is sample output, not a heading — the
        # guide quotes the prompt's own section headings verbatim.
        if line.startswith("```"):
            fenced = not fenced
            continue
        if fenced:
            continue
        m = re.match(r"^#{1,6}\s+(.*?)\s*$", line)
        if m:
            ids[slug(m.group(1))] = m.group(1)
    return ids


def main() -> int:
    for text, want in SELF_TEST.items():
        if slug(text) != want:
            print(f"the slug rule is wrong before it checked anything: {text!r} -> {slug(text)!r}, "
                  f"but the site publishes {want!r}", file=sys.stderr)
            return 1

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
