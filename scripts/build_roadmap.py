#!/usr/bin/env python3
"""Build the Passage roadmap from roadmap/waves.json + roadmap/phases.json.

  python3 scripts/build_roadmap.py            # validate + regenerate roadmap/*.md
  python3 scripts/build_roadmap.py --github   # also create labels, milestones, issues
                                              # and add them to the GitHub Project

GitHub mode is idempotent: it looks up existing milestones/issues by title before
creating anything, so it can be re-run after editing waves.json.
"""
import argparse
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
ROADMAP = ROOT / "roadmap"
REPO = "BenjaminSRussell/Passage"
PROJECT_OWNER = "BenjaminSRussell"
PROJECT_NUMBER = 5


def load():
    phases = json.loads((ROADMAP / "phases.json").read_text())
    waves = json.loads((ROADMAP / "waves.json").read_text())
    return phases, waves


def validate(phases, waves):
    errors = []
    nums = [w["n"] for w in waves]
    if nums != list(range(1, 51)):
        errors.append(f"waves must be numbered 1..50 in order, got {nums[:5]}...{nums[-3:]}")
    by_phase = {p["phase"]: p for p in phases}
    for w in waves:
        p = by_phase.get(w["phase"])
        if not p:
            errors.append(f"wave {w['n']}: unknown phase {w['phase']}")
            continue
        lo, hi = p["waves"]
        if not lo <= w["n"] <= hi:
            errors.append(f"wave {w['n']} is tagged phase {w['phase']} but that phase spans {lo}-{hi}")
        for d in w["depends_on"]:
            if d >= w["n"]:
                errors.append(f"wave {w['n']} depends on {d}, which is not earlier (must be sequential)")
        for key in ("goal", "replaces", "deliverable", "proof", "deploy", "references"):
            if not w.get(key):
                errors.append(f"wave {w['n']} missing {key}")
        if not (w["replaces"].get("ax") and w["replaces"].get("sap")):
            errors.append(f"wave {w['n']} must name both the AX and SAP features it replaces")
    if errors:
        for e in errors:
            print("ERROR:", e, file=sys.stderr)
        sys.exit(1)


def wave_title(w):
    return f"Wave {w['n']:02d}: {w['name']}"


def phase_title(p):
    return f"Phase {p['phase']}: {p['name']} (waves {p['waves'][0]}-{p['waves'][1]})"


def wave_md(w, phases):
    p = next(p for p in phases if p["phase"] == w["phase"])
    lines = [f"## {wave_title(w)}", "", f"**Phase {p['phase']} — {p['name']}**", "", f"**Goal.** {w['goal']}", ""]
    lines += ["**Replaces**", "", f"- *AX / D365:* {w['replaces']['ax']}", f"- *SAP:* {w['replaces']['sap']}", ""]
    lines += ["**Deliverable**", ""] + [f"- {d}" for d in w["deliverable"]] + [""]
    lines += ["**Proof (testable acceptance)**", ""] + [f"- [ ] {t}" for t in w["proof"]] + [""]
    lines += ["**Deploy**", "", w["deploy"], ""]
    lines += ["**References**", ""] + [f"- [{r['name']}]({r['url']})" for r in w["references"]] + [""]
    deps = ", ".join(f"wave {d}" for d in w["depends_on"]) or "none (first wave)"
    lines += [f"**Depends on:** {deps}", ""]
    return "\n".join(lines)


def write_markdown(phases, waves):
    index = ["# Passage Roadmap — 50 Waves", "", "Generated from `waves.json` and `phases.json` by `scripts/build_roadmap.py`. Edit the JSON, not this file.", "",
             "Each wave ships a **deliverable**, a **proof** (automated tests/fixtures/perf targets), a **deploy** (what runs where), and **references** (AX/SAP docs and standards/open-source we build against). Waves are strictly sequential; every 5th wave is a phase-exit with stakeholder sign-off. Wave 30 is the first go-live candidate; wave 50 is production go-live and v1.0.", ""]
    for p in phases:
        index += [f"## {phase_title(p)}", "", p["intent"], "", "| Wave | Name | Goal |", "|---|---|---|"]
        for w in waves:
            if w["phase"] == p["phase"]:
                index.append(f"| {w['n']} | [{w['name']}](phase-{p['phase']}-{p['slug']}.md#wave-{w['n']:02d}-{w['slug']}) | {w['goal']} |")
        index.append("")
        body = [f"# {phase_title(p)}", "", p["intent"], ""]
        for w in waves:
            if w["phase"] == p["phase"]:
                body.append(wave_md(w, phases))
                body.append("---\n")
        (ROADMAP / f"phase-{p['phase']}-{p['slug']}.md").write_text("\n".join(body))
    (ROADMAP / "README.md").write_text("\n".join(index))


# ---------------------------------------------------------------- GitHub

def gh(*args, **kw):
    r = subprocess.run(["gh", *args], capture_output=True, text=True, **kw)
    if r.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args)}\n{r.stderr}")
    return r.stdout.strip()


def gh_json(*args):
    out = gh(*args)
    return json.loads(out) if out else []


def ensure_labels(phases):
    existing = {l["name"] for l in gh_json("label", "list", "-R", REPO, "--limit", "200", "--json", "name")}
    wanted = {"wave": "0E8A16", "phase-exit": "D93F0B", "go-live": "B60205"}
    for p in phases:
        wanted[f"phase-{p['phase']}"] = "1D76DB"
    for name, color in wanted.items():
        if name not in existing:
            gh("label", "create", name, "-R", REPO, "--color", color)


def ensure_milestones(phases):
    existing = {m["title"]: m["number"] for m in gh_json("api", f"repos/{REPO}/milestones?state=all&per_page=100")}
    out = {}
    for p in phases:
        t = phase_title(p)
        if t in existing:
            out[p["phase"]] = existing[t]
        else:
            m = gh_json("api", "-X", "POST", f"repos/{REPO}/milestones", "-f", f"title={t}", "-f", f"description={p['intent']}")
            out[p["phase"]] = m["number"]
    return out


def ensure_issues(phases, waves, milestones):
    existing = {i["title"]: i["number"] for i in gh_json("issue", "list", "-R", REPO, "--state", "all", "--limit", "500", "--json", "title,number")}
    numbers = {}
    for w in waves:
        t = wave_title(w)
        if t in existing:
            numbers[w["n"]] = existing[t]
            continue
        p = next(p for p in phases if p["phase"] == w["phase"])
        labels = ["wave", f"phase-{p['phase']}"]
        if w["n"] % 5 == 0:
            labels.append("phase-exit")
        if w["n"] in (30, 50):
            labels.append("go-live")
        body = wave_md(w, phases).split("\n", 2)[2]  # drop the H2 title line
        body += "\n\n**Definition of Done:** all Proof boxes checked with CI links, Argo CD app healthy in stage, references/ADRs linked, phase-exit demo recorded if applicable."
        url = gh("issue", "create", "-R", REPO, "--title", t, "--body", body, "--label", ",".join(labels), "--milestone", str(milestones[w["phase"]]))
        numbers[w["n"]] = int(url.rsplit("/", 1)[1])
        print("created", t, url)
    return numbers


def ensure_project_fields(phases):
    fields = {f["name"]: f for f in gh_json("project", "field-list", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--format", "json", "--limit", "100")["fields"]}
    if "Phase" not in fields:
        opts = ",".join(f"P{p['phase']} {p['name']}" for p in phases)
        gh("project", "field-create", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--name", "Phase", "--data-type", "SINGLE_SELECT", "--single-select-options", opts)
    if "Wave" not in fields:
        gh("project", "field-create", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--name", "Wave", "--data-type", "NUMBER")
    if "Gate" not in fields:
        gh("project", "field-create", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--name", "Gate", "--data-type", "SINGLE_SELECT", "--single-select-options", "Not started,Deliverable,Proof,Deploy,Signed off")
    return {f["name"]: f for f in gh_json("project", "field-list", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--format", "json", "--limit", "100")["fields"]}


def ensure_project_items(phases, waves, issue_numbers, fields):
    proj = gh_json("project", "view", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--format", "json")
    project_id = proj["id"]
    items = gh_json("project", "item-list", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--format", "json", "--limit", "500")["items"]
    by_title = {i.get("title"): i for i in items}
    phase_opts = {o["name"]: o["id"] for o in fields["Phase"]["options"]}
    gate_opts = {o["name"]: o["id"] for o in fields["Gate"]["options"]}
    for w in waves:
        t = wave_title(w)
        item = by_title.get(t)
        if not item:
            url = f"https://github.com/{REPO}/issues/{issue_numbers[w['n']]}"
            item = gh_json("project", "item-add", str(PROJECT_NUMBER), "--owner", PROJECT_OWNER, "--url", url, "--format", "json")
            print("added to project", t)
        p = next(p for p in phases if p["phase"] == w["phase"])
        base = ["project", "item-edit", "--project-id", project_id, "--id", item["id"]]
        gh(*base, "--field-id", fields["Phase"]["id"], "--single-select-option-id", phase_opts[f"P{p['phase']} {p['name']}"])
        gh(*base, "--field-id", fields["Wave"]["id"], "--number", str(w["n"]))
        if not item.get("gate"):
            gh(*base, "--field-id", fields["Gate"]["id"], "--single-select-option-id", gate_opts["Not started"])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--github", action="store_true", help="sync labels, milestones, issues and project items")
    args = ap.parse_args()
    phases, waves = load()
    validate(phases, waves)
    write_markdown(phases, waves)
    print(f"ok: {len(waves)} waves across {len(phases)} phases; markdown regenerated")
    if args.github:
        ensure_labels(phases)
        milestones = ensure_milestones(phases)
        numbers = ensure_issues(phases, waves, milestones)
        fields = ensure_project_fields(phases)
        ensure_project_items(phases, waves, numbers, fields)
        print("github sync complete")


if __name__ == "__main__":
    main()
