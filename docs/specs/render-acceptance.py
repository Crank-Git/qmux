#!/usr/bin/env python3
"""Render docs/acceptance.md from the feature files and the proof table.

    python3 docs/specs/render-acceptance.py            # check, then write docs/acceptance.md
    python3 docs/specs/render-acceptance.py --check    # check only, write nothing
    python3 docs/specs/render-acceptance.py --skeleton # print proof entries for unmapped criteria

The script reads each item of the `## Acceptance criteria` section of every
docs/specs/features/*.md file. It joins the wrapped lines of an item with one
space and removes a leading `[ ] ` task-list marker. It changes no other text.

docs/specs/acceptance-proofs.json holds the proof of each criterion, keyed by
the feature file and the criterion text. The check fails when:

- a criterion has no entry, or an entry names a criterion that no feature file holds;
- a named test function does not exist in the named directory;
- a named doc section is not a heading of the named file;
- a named CI job is not a job of the named workflow file;
- a named file does not exist.

An entry with an empty `proofs` list must give a `gap`. The map lists it as a
criterion with no proof. The check does not fail for a gap, because a gap is a
finding for the maintainer and not an error of the map.
"""

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FEATURES = ROOT / "docs" / "specs" / "features"
PROOFS = ROOT / "docs" / "specs" / "acceptance-proofs.json"
OUTPUT = ROOT / "docs" / "acceptance.md"


def front_matter(text):
    """Return the key-value pairs of the YAML front matter, as strings."""
    fields = {}
    if not text.startswith("---\n"):
        return fields
    for line in text.split("\n---\n", 1)[0].splitlines()[1:]:
        key, sep, value = line.partition(":")
        if sep and not line.startswith(" "):
            fields[key.strip()] = value.strip().strip('"')
    return fields


def criteria(text):
    """Return the items of the `## Acceptance criteria` section, one string each."""
    items = []
    inside = False
    for line in text.splitlines():
        if line.startswith("## "):
            inside = line.strip() == "## Acceptance criteria"
            continue
        if not inside:
            continue
        if line.startswith("- "):
            item = line[2:]
            if item.startswith("[ ] ") or item.startswith("[x] "):
                item = item[4:]
            items.append(item.strip())
        elif line.startswith("  ") and line.strip() and items:
            items[-1] += " " + line.strip()
    return items


def load_features():
    """Return (file name, title, criteria) for each feature file, in file order."""
    features = []
    for path in sorted(FEATURES.glob("*.md")):
        text = path.read_text(encoding="utf-8")
        title = front_matter(text).get("feature", path.stem)
        features.append((path.name, title, criteria(text)))
    return features


def heading_slug(heading):
    """Return the GitHub anchor of a heading."""
    slug = heading.strip().lower()
    slug = re.sub(r"[^\w\- ]", "", slug)
    return slug.replace(" ", "-")


def headings(path):
    """Return the heading texts of a markdown file, with the leading # marks removed."""
    found = []
    fenced = False
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.startswith("```"):
            fenced = not fenced
        elif not fenced and re.match(r"#{1,6} ", line):
            found.append(line.lstrip("#").strip())
    return found


def test_exists(directory, name):
    """Return true when a _test.go file in the directory declares the function."""
    pattern = re.compile(r"^func " + re.escape(name) + r"\(", re.MULTILINE)
    for path in (ROOT / directory).glob("*_test.go"):
        if pattern.search(path.read_text(encoding="utf-8")):
            return True
    return False


def job_exists(workflow, job):
    """Return true when the workflow file declares the job under `jobs:`."""
    text = (ROOT / workflow).read_text(encoding="utf-8")
    return re.search(r"^  " + re.escape(job) + r":\s*$", text, re.MULTILINE) is not None


def check_proof(proof):
    """Return an error string for a proof that does not resolve, or None."""
    if "test" in proof:
        name = proof["name"].split("/", 1)[0]
        if not (ROOT / proof["test"]).is_dir():
            return f"no directory {proof['test']}"
        if not test_exists(proof["test"], name):
            return f"no func {name} in {proof['test']}"
    elif "doc" in proof:
        path = ROOT / proof["doc"]
        if path == OUTPUT:
            # The script writes this file after the check, so the file can be absent here.
            return None
        if not path.is_file():
            return f"no file {proof['doc']}"
        if "section" in proof and proof["section"] not in headings(path):
            return f"no heading '{proof['section']}' in {proof['doc']}"
    elif "ci" in proof:
        if not (ROOT / proof["ci"]).is_file():
            return f"no file {proof['ci']}"
        if not job_exists(proof["ci"], proof["job"]):
            return f"no job {proof['job']} in {proof['ci']}"
    elif "file" in proof:
        if not (ROOT / proof["file"]).exists():
            return f"no file {proof['file']}"
    else:
        return f"unknown proof kind {sorted(proof)}"
    return None


def check(features, proofs):
    """Return the list of errors between the feature files and the proof table."""
    errors = []
    for name, _title, items in features:
        entries = proofs.get(name, [])
        known = {entry["criterion"] for entry in entries}
        for item in items:
            if item not in known:
                errors.append(f"{name}: no proof entry for: {item}")
        for entry in entries:
            if entry["criterion"] not in items:
                errors.append(f"{name}: entry names no criterion: {entry['criterion']}")
            if not entry["proofs"] and not entry.get("gap"):
                errors.append(f"{name}: entry has no proof and no gap: {entry['criterion']}")
            for proof in entry["proofs"]:
                error = check_proof(proof)
                if error:
                    errors.append(f"{name}: {error}: {entry['criterion']}")
    for name in proofs:
        if name not in {feature[0] for feature in features}:
            errors.append(f"proof table names no feature file: {name}")
    return errors


def link(path, anchor=""):
    """Return a link target relative to docs/acceptance.md."""
    target = Path("..") / path if not path.startswith("docs/") else Path(path[len("docs/"):])
    return target.as_posix() + (f"#{anchor}" if anchor else "")


def render_proof(proof):
    """Return the markdown text of one proof."""
    if "test" in proof:
        text = f"`{proof['name']}` in [`{proof['test']}`]({link(proof['test'])})"
    elif "doc" in proof:
        if "section" in proof:
            text = f"[`{proof['doc']}` § {proof['section']}]({link(proof['doc'], heading_slug(proof['section']))})"
        else:
            text = f"[`{proof['doc']}`]({link(proof['doc'])})"
    elif "ci" in proof:
        text = f"job `{proof['job']}` in [`{proof['ci']}`]({link(proof['ci'])})"
    else:
        text = f"[`{proof['file']}`]({link(proof['file'])})"
    if proof.get("note"):
        text += f" ({proof['note']})"
    return text


def cell(text):
    """Return text that is safe in one markdown table cell."""
    return text.replace("|", "\\|")


def render(features, proofs):
    """Return the text of docs/acceptance.md."""
    total = sum(len(items) for _n, _t, items in features)
    gaps = []
    lines = [
        "# Acceptance map",
        "",
        "<!-- Generated by docs/specs/render-acceptance.py from docs/specs/features/*.md",
        "     and docs/specs/acceptance-proofs.json. Do not edit this file by hand. -->",
        "",
        "This map quotes each acceptance criterion of the approved spec, `docs/specs/features/*.md`.",
        "For each criterion, it names the test, the doc section, the CI job or the committed file that proves it.",
        "The text of each criterion is verbatim.",
        "The script joins the wrapped lines of a criterion and removes the `[ ]` task-list marker.",
        "",
        f"The spec holds **{total}** acceptance criteria in {len(features)} feature files.",
        "",
        "To update the map, edit `docs/specs/acceptance-proofs.json`, then run this command:",
        "",
        "```bash",
        "python3 docs/specs/render-acceptance.py",
        "```",
        "",
        "The script fails when a criterion has no entry, or when a named test, section, job or file does not exist.",
        "",
        "A test proof names the Go test function and the directory of its package.",
        "To run one, use `go test ./<directory> -run '^<name>$'`.",
        "Tests in `tests/interoperability` belong to a separate module: run them from that directory.",
        "",
        "| Feature file | Criteria | With a proof |",
        "|---|---|---|",
    ]
    for name, title, items in features:
        entries = {entry["criterion"]: entry for entry in proofs.get(name, [])}
        proven = sum(1 for item in items if entries.get(item, {}).get("proofs"))
        lines.append(f"| [{title}](#{heading_slug(title)}) | {len(items)} | {proven} |")
    for name, title, items in features:
        entries = {entry["criterion"]: entry for entry in proofs.get(name, [])}
        lines += [
            "",
            f"## {title}",
            "",
            f"Criteria from [`docs/specs/features/{name}`](specs/features/{name}#acceptance-criteria).",
            "",
            "| # | Criterion | Proof |",
            "|---|---|---|",
        ]
        for number, item in enumerate(items, 1):
            entry = entries.get(item, {"proofs": [], "gap": "No entry."})
            parts = [render_proof(proof) for proof in entry["proofs"]]
            if entry.get("ruling"):
                parts.append(f"Ruling: {entry['ruling']}")
            if not entry["proofs"]:
                parts.insert(0, f"**No proof.** {entry['gap']}")
                gaps.append((title, number, item, entry["gap"]))
            lines.append(f"| {number} | {cell(item)} | {cell('<br>'.join(parts))} |")
    lines += ["", "## Criteria with no proof", ""]
    if gaps:
        lines += [
            "Each criterion below has no test, doc section, CI job or committed file that proves it.",
            "",
            "| Feature | # | Criterion | Reason |",
            "|---|---|---|---|",
        ]
        for title, number, item, gap in gaps:
            lines.append(f"| {title} | {number} | {cell(item)} | {cell(gap)} |")
    else:
        lines.append("Every criterion has a proof.")
    return "\n".join(lines) + "\n"


def skeleton(features, proofs):
    """Return proof entries, as JSON, for each criterion that has no entry."""
    missing = {}
    for name, _title, items in features:
        known = {entry["criterion"] for entry in proofs.get(name, [])}
        new = [{"criterion": item, "proofs": []} for item in items if item not in known]
        if new:
            missing[name] = new
    return json.dumps(missing, indent=2, ensure_ascii=False)


def main():
    features = load_features()
    proofs = json.loads(PROOFS.read_text(encoding="utf-8")) if PROOFS.exists() else {}
    if "--skeleton" in sys.argv:
        print(skeleton(features, proofs))
        return 0
    errors = check(features, proofs)
    for error in errors:
        print(error, file=sys.stderr)
    if errors:
        print(f"{len(errors)} errors", file=sys.stderr)
        return 1
    if "--check" not in sys.argv:
        OUTPUT.write_text(render(features, proofs), encoding="utf-8")
        print(f"wrote {OUTPUT.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
