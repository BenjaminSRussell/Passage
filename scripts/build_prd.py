#!/usr/bin/env python3
"""Assemble the full Passage PRD from docs/PRD.md + docs/prd/*.md.

  python3 scripts/build_prd.py          # writes docs/PRD-full.md, prints word/page counts
  python3 scripts/build_prd.py --pdf    # also renders docs/PRD-full.pdf via pandoc if installed

Page estimate uses 500 words/page (typical for a dense A4/Letter technical document).
"""
import argparse
import pathlib
import re
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"
PARTS_DIR = DOCS / "prd"
WORDS_PER_PAGE = 500

# Order of assembly. The master PRD first, then the thesis, then one part per phase, then appendices.
ORDER = [
    DOCS / "PRD.md",
    PARTS_DIR / "part-00-thesis.md",
    PARTS_DIR / "phase-0-foundation.md",
    PARTS_DIR / "phase-1-master-data.md",
    PARTS_DIR / "phase-2-financial-core.md",
    PARTS_DIR / "phase-3-procure-to-pay.md",
    PARTS_DIR / "phase-4-manufacturing.md",
    PARTS_DIR / "phase-5-order-to-cash.md",
    PARTS_DIR / "phase-6-logistics.md",
    PARTS_DIR / "phase-7-treasury-controlling.md",
    PARTS_DIR / "phase-8-projects-people-service.md",
    PARTS_DIR / "phase-9-intelligence-golive.md",
    PARTS_DIR / "appendices.md",
    PARTS_DIR / "coverage-ax.md",
    PARTS_DIR / "coverage-sap.md",
]


def words(text):
    return len(re.findall(r"\S+", text))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pdf", action="store_true")
    args = ap.parse_args()

    chunks, total, missing = [], 0, []
    rows = []
    for path in ORDER:
        if not path.exists():
            missing.append(path.relative_to(ROOT))
            continue
        text = path.read_text()
        n = words(text)
        total += n
        rows.append((path.relative_to(ROOT), n, n / WORDS_PER_PAGE))
        chunks.append(text.rstrip() + "\n\n\\newpage\n\n")
    out = DOCS / "PRD-full.md"
    out.write_text("".join(chunks))

    for rel, n, pages in rows:
        print(f"{str(rel):55s} {n:7d} words  ~{pages:5.1f} pages")
    print(f"{'TOTAL':55s} {total:7d} words  ~{total / WORDS_PER_PAGE:5.1f} pages  -> {out.relative_to(ROOT)}")
    if missing:
        print("missing parts:", ", ".join(map(str, missing)), file=sys.stderr)

    if args.pdf:
        render_pdf(out)


CSS = """
body{font-family:-apple-system,Helvetica,Arial,sans-serif;font-size:10.5pt;line-height:1.4;max-width:none;margin:0;color:#111}
h1{font-size:20pt;border-bottom:2px solid #333;padding-bottom:4px;margin-top:28pt;page-break-before:always}
h1:first-of-type{page-break-before:avoid}
h2{font-size:15pt;margin-top:20pt;border-bottom:1px solid #999}
h3{font-size:12.5pt;margin-top:14pt} h4{font-size:11pt}
table{border-collapse:collapse;width:100%;font-size:9pt;margin:8pt 0;page-break-inside:auto}
th,td{border:1px solid #bbb;padding:3px 5px;vertical-align:top;text-align:left}
th{background:#eee} tr{page-break-inside:avoid}
code{font-family:Menlo,monospace;font-size:8.5pt;background:#f4f4f4;padding:0 2px}
pre{background:#f4f4f4;padding:6px;font-size:8.5pt;white-space:pre-wrap;border:1px solid #ddd}
@page{size:A4;margin:16mm 14mm}
"""

CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"


def render_pdf(md_path):
    """Markdown -> HTML (marked, via npx) -> PDF (headless Chrome). No pandoc/LaTeX needed."""
    html_path = md_path.with_suffix(".html")
    pdf_path = md_path.with_suffix(".pdf")
    body = subprocess.run(["npx", "--yes", "marked", "--gfm", "-i", str(md_path)], capture_output=True, text=True, check=True).stdout
    body = body.replace("\\newpage", '<div style="page-break-after:always"></div>')
    html_path.write_text(f"<!doctype html><html><head><meta charset='utf-8'><title>Passage PRD</title><style>{CSS}</style></head><body>{body}</body></html>")
    chrome = CHROME if pathlib.Path(CHROME).exists() else shutil.which("google-chrome") or shutil.which("chromium")
    if not chrome:
        print("Chrome not found; wrote HTML only:", html_path.relative_to(ROOT), file=sys.stderr)
        return
    subprocess.run([chrome, "--headless=new", "--disable-gpu", "--no-pdf-header-footer", f"--print-to-pdf={pdf_path}", f"file://{html_path}"], capture_output=True, text=True, check=True)
    pages = len(re.findall(rb"/Type\s*/Page[^s]", pdf_path.read_bytes()))
    print(f"wrote {pdf_path.relative_to(ROOT)} ({pages} pages)")


if __name__ == "__main__":
    main()
