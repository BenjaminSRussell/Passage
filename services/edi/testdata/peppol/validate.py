"""Validate the peppol-invoices-50 UBL against the official Peppol BIS Billing 3.0
schematron (CEN EN 16931 UBL rules and the Peppol rules), independent of the Go
engine that produced the XML.

The rules are the published OpenPeppol files of release 3.0.21, pinned by
SHA-256. They are compiled to XSLT by the small ISO Schematron compiler below
and run with Saxon-HE (`saxonche`, the only non-stdlib dependency):

    python3 -m venv /tmp/v && /tmp/v/bin/pip install saxonche==13.1.1
    /tmp/v/bin/python -I validate.py --fetch            # downloads + checks the rules
    /tmp/v/bin/python -I validate.py --selftest         # official examples and unit tests
    /tmp/v/bin/python -I validate.py --ubl-xsd DIR      # writes schematron-report.json

`--ubl-xsd` is the xsd/ folder of the OASIS UBL 2.1 package
(https://docs.oasis-open.org/ubl/os-UBL-2.1/UBL-2.1.zip, SHA-256
60b80d76394a8a2add90723ecb8e0e2e9d826775de9749df37a72d60703f86ed); each file is
also validated against UBL-Invoice-2.1.xsd or UBL-CreditNote-2.1.xsd with
xmllint, because schematron does not check element order.

`--selftest` runs the compiler on the official examples (no fatal) and on the
official unit-test cases (each listed <assert> must fire, each <success> must
not), so the compiler is checked against OpenPeppol's own expectations before
it is trusted with our files.

The report records the SHA-256 of every XML file it validated; the Go test
TestPeppol50_MatchesSchematronReport fails when the engine's output no longer
matches what was validated here.
"""

import argparse
import hashlib
import io
import json
import pathlib
import re
import subprocess
import sys
import urllib.request
import xml.etree.ElementTree as ET

TAG = "3.0.21"
BASE = "https://raw.githubusercontent.com/OpenPEPPOL/peppol-bis-invoice-3/" + TAG + "/"
RULES = {
    "CEN-EN16931-UBL.sch": "268d4f7a2688676695e6c69cba6fba69a6802604fee12cb544a6b30ff09555a3",
    "PEPPOL-EN16931-UBL.sch": "9616cfffb418f349b532c1e25d3c831b8af6a47f4f5607ca0df4b1336f48e5f3",
}
SCH = "http://purl.oclc.org/dsdl/schematron"
XSL = "http://www.w3.org/1999/XSL/Transform"
HERE = pathlib.Path(__file__).resolve().parent


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def fetch(path, dest):
    with urllib.request.urlopen(BASE + path, timeout=60) as resp:
        data = resp.read()
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_bytes(data)
    return data


def load_rules(cache, download):
    out = {}
    for name, want in RULES.items():
        path = cache / name
        if download:
            data = fetch("rules/sch/" + name, path)
        elif path.exists():
            data = path.read_bytes()
        else:
            sys.exit(f"{path} missing: run with --fetch")
        if sha256(data) != want:
            sys.exit(f"{name}: sha256 {sha256(data)} is not the pinned {want}")
        out[name] = data
    return out


def esc(s):
    return s.replace("&", "&amp;").replace('"', "&quot;").replace("<", "&lt;").replace(">", "&gt;")


def compile_schematron(data):
    """Compiles an ISO Schematron (queryBinding xslt2, no abstract patterns or
    includes, which these files do not use) to an XSLT 3.0 stylesheet that
    reports failed asserts as <failed id flag location text/>. Each pattern is a
    mode; inside a pattern a node is handled by the first matching rule in
    document order (descending priority), as ISO Schematron requires. The
    input is the pinned OpenPeppol files (SHA-256 checked), not untrusted XML."""
    prefixes = {}
    for _event, (prefix, uri) in ET.iterparse(io.BytesIO(data), events=["start-ns"]):
        if prefix:
            prefixes[prefix] = uri
    root = ET.fromstring(data)
    for ns in root.findall(f"{{{SCH}}}ns"):
        prefixes[ns.get("prefix")] = ns.get("uri")
    prefixes.setdefault("xs", "http://www.w3.org/2001/XMLSchema")
    ET.register_namespace("xsl", XSL)

    decls = " ".join(f'xmlns:{p}="{esc(u)}"' for p, u in sorted(prefixes.items()) if p not in ("xsl",))
    out = [f'<xsl:stylesheet version="3.0" xmlns:xsl="{XSL}" {decls} exclude-result-prefixes="#all">']
    for fn in root.findall(f"{{{XSL}}}function"):
        out.append(ET.tostring(fn, encoding="unicode"))
    patterns = root.findall(f"{{{SCH}}}pattern")
    # Schema and pattern lets are global variables, as in the ISO skeleton, so
    # rule contexts (XSLT match patterns) may use them; names must be unique.
    seen = set()
    for let in root.findall(f"{{{SCH}}}let") + [l for p in patterns for l in p.findall(f"{{{SCH}}}let")]:
        if let.get("name") in seen:
            raise SystemExit(f"duplicate let {let.get('name')}")
        seen.add(let.get("name"))
        out.append(f'<xsl:variable name="{let.get("name")}" select="{esc(let.get("value"))}"/>')
    out.append('<xsl:template match="/"><report>')
    for i, _ in enumerate(patterns):
        out.append(f'<xsl:apply-templates select="/" mode="M{i}"/>')
    out.append("</report></xsl:template>")
    for i, pattern in enumerate(patterns):
        rules = pattern.findall(f"{{{SCH}}}rule")
        for j, rule in enumerate(rules):
            body = []
            for let in rule.findall(f"{{{SCH}}}let"):
                body.append(f'<xsl:variable name="{let.get("name")}" select="{esc(let.get("value"))}"/>')
            for a in rule:
                if a.tag not in (f"{{{SCH}}}assert", f"{{{SCH}}}report"):
                    continue
                if any(True for _ in a):
                    raise SystemExit("assert with child elements is not supported by this compiler")
                text = " ".join((a.text or "").split())
                test = a.get("test")
                cond = f"not({test})" if a.tag.endswith("assert") else f"({test})"
                body.append(
                    f'<xsl:if test="{esc(cond)}"><failed id="{esc(a.get("id", ""))}" flag="{esc(a.get("flag", "fatal"))}" '
                    f'location="{{path()}}" text="{esc(text).replace("{", "{{").replace("}", "}}")}"/></xsl:if>'
                )
            body.append(f'<xsl:apply-templates select="@*|*" mode="M{i}"/>')
            out.append(
                f'<xsl:template match="{esc(rule.get("context"))}" mode="M{i}" priority="{1000 - j}">' + "".join(body) + "</xsl:template>"
            )
        out.append(f'<xsl:template match="text()" mode="M{i}" priority="-1"/>')
        out.append(f'<xsl:template match="@*|node()" mode="M{i}" priority="-2"><xsl:apply-templates select="@*|*" mode="M{i}"/></xsl:template>')
    out.append("</xsl:stylesheet>")
    return "\n".join(out)


class Validator:
    def __init__(self, rules):
        from saxonche import PySaxonProcessor

        self.proc = PySaxonProcessor(license=False)
        self.xslt = self.proc.new_xslt30_processor()
        self.exes = {}
        for name, data in rules.items():
            self.exes[name] = self.xslt.compile_stylesheet(stylesheet_text=compile_schematron(data))

    def run(self, xml_text):
        """Returns every failed assert as (rules file, id, flag, location, text)."""
        doc = self.proc.parse_xml(xml_text=xml_text)
        found = []
        for name, exe in self.exes.items():
            result = exe.transform_to_string(xdm_node=doc)
            for f in ET.fromstring(result).iter("failed"):
                found.append((name, f.get("id"), f.get("flag"), f.get("location"), f.get("text")))
        return found


def selftest(validator, cache):
    """Official examples must have no fatal failure; in the official unit tests
    every <assert> id must fire on its case and every <success> id must not."""
    examples = ["base-example.xml", "base-creditnote-correction.xml", "Allowance-example.xml", "vat-category-E.xml",
                "vat-category-O.xml", "vat-category-Z.xml", "Vat-category-S.xml", "base-negative-inv-correction.xml"]
    bad = 0
    for name in examples:
        path = cache / "examples" / name
        if not path.exists():
            try:
                fetch("rules/examples/" + name, path)
            except Exception as e:  # an example renamed upstream is skipped, not trusted
                print(f"skip example {name}: {e}")
                continue
        fatal = [f for f in validator.run(path.read_text(encoding="utf-8")) if f[2] == "fatal"]
        print(f"example {name}: {len(fatal)} fatal")
        bad += bool(fatal)
    # Mutations of the official base example that the CEN rules must catch.
    base = (cache / "examples" / "base-example.xml").read_text(encoding="utf-8")
    payable = re.sub(r'(<cbc:PayableAmount currencyID="\w+">)[^<]+', r"\g<1>1.00", base)
    inclusive = re.sub(r'(<cbc:TaxInclusiveAmount currencyID="\w+">)[^<]+', r"\g<1>1.00", base)
    for rid, doc in (("BR-CO-16", payable), ("BR-CO-15", inclusive)):
        if rid not in {f[1] for f in validator.run(doc)}:
            print(f"mutation: expected {rid} to fire")
            bad += 1
    units = ["PEPPOL-EN16931-R001.xml", "PEPPOL-EN16931-R003.xml", "PEPPOL-EN16931-R004.xml", "PEPPOL-EN16931-R020.xml",
             "PEPPOL-EN16931-R040.xml", "PEPPOL-EN16931-R041.xml", "PEPPOL-EN16931-R053.xml", "PEPPOL-EN16931-R080.xml",
             "PEPPOL-EN16931-R100.xml", "PEPPOL-EN16931-R110.xml", "PEPPOL-EN16931-R120.xml", "PEPPOL-EN16931-R121.xml",
             "PEPPOL-EN16931-R130.xml", "PEPPOL-EN16931-P0100.xml", "PEPPOL-EN16931-P0101.xml", "PEPPOL-EN16931-CL001.xml",
             "PEPPOL-EN16931-CL007.xml", "PEPPOL-COMMON-R040.xml", "PEPPOL-COMMON-R043.xml", "PEPPOL-COMMON-R049.xml"]
    cases = fires = quiets = 0
    for name in units:
        path = cache / "unit-UBL-PEPPOL" / name
        if not path.exists():
            try:
                fetch("rules/unit-UBL-PEPPOL/" + name, path)
            except Exception as e:
                print(f"skip unit {name}: {e}")
                continue
        text = path.read_text(encoding="utf-8")
        for m in re.finditer(r"<test>(.*?)</test>", text, re.S):
            block = m.group(1)
            ids = re.search(r"<assert>(.*?)</assert>", block, re.S)
            want_fire = re.findall(r"<error[^>]*>([^<]+)</error>|<warning[^>]*>([^<]+)</warning>", ids.group(1)) if ids else []
            want_fire = [a or b for a, b in want_fire]
            want_quiet = re.findall(r"<success[^>]*>([^<]+)</success>", ids.group(1)) if ids else []
            doc = re.search(r"(<(?:Invoice|CreditNote)\b.*?</(?:Invoice|CreditNote)>)", block, re.S)
            if not doc:
                continue
            fired = {f[1] for f in validator.run(doc.group(1))}
            cases += 1
            fires += len(want_fire)
            quiets += len(want_quiet)
            for rid in want_fire:
                if rid.strip() not in fired:
                    print(f"unit {name}: expected {rid} to fire")
                    bad += 1
            for rid in want_quiet:
                if rid.strip() in fired:
                    print(f"unit {name}: expected {rid} not to fire")
                    bad += 1
    print(f"unit cases checked: {cases} ({fires} expected to fire, {quiets} expected quiet); failures: {bad}")
    return bad


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fetch", action="store_true", help="download the pinned rules into --cache")
    ap.add_argument("--selftest", action="store_true", help="check the compiler on official examples and unit tests")
    ap.add_argument("--cache", default=str(HERE / ".rules-cache"))
    ap.add_argument("--xml", default=str(HERE / "xml"))
    ap.add_argument("--report", default=str(HERE / "schematron-report.json"))
    ap.add_argument("--ubl-xsd", help="xsd/ folder of the OASIS UBL 2.1 package; validates each file with xmllint")
    args = ap.parse_args()
    cache = pathlib.Path(args.cache)
    rules = load_rules(cache, args.fetch)
    validator = Validator(rules)
    if args.selftest:
        sys.exit(1 if selftest(validator, cache) else 0)

    results = []
    for path in sorted(pathlib.Path(args.xml).glob("*.xml")):
        data = path.read_bytes()
        found = validator.run(data.decode("utf-8"))
        xsd = None
        if args.ubl_xsd:
            main_doc = "UBL-CreditNote-2.1.xsd" if b"<CreditNote " in data else "UBL-Invoice-2.1.xsd"
            run = subprocess.run(["xmllint", "--noout", "--schema", str(pathlib.Path(args.ubl_xsd) / "maindoc" / main_doc), str(path)],
                                 capture_output=True, text=True)
            xsd = run.returncode == 0
            if not xsd:
                print(run.stderr.strip())
        results.append({
            "xsdValid": xsd,
            "file": path.name,
            "sha256": sha256(data),
            "fatal": sorted({f"{f[1]}" for f in found if f[2] == "fatal"}),
            "warnings": sorted({f"{f[1]}" for f in found if f[2] != "fatal"}),
        })
    passed = sum(1 for r in results if not r["fatal"])
    report = {
        "rules": {"release": TAG, "files": RULES},
        "validator": "ISO Schematron compiled by validate.py, run with Saxon-HE (saxonche)",
        "xsd": "OASIS UBL 2.1 (xmllint)" if args.ubl_xsd else None,
        "documents": len(results),
        "passed": passed,
        "results": results,
    }
    pathlib.Path(args.report).write_text(json.dumps(report, indent=1) + "\n", encoding="utf-8")
    print(f"{passed}/{len(results)} documents pass with no fatal schematron assertion")
    for r in results:
        if r["fatal"]:
            print(f"  {r['file']}: {', '.join(r['fatal'])}")


if __name__ == "__main__":
    main()
