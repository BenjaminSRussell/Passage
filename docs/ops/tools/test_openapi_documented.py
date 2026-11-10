"""Every service's OpenAPI file parses and has no undocumented operation (PSG-50, AC-50.11, issue #50 subtask 50-4).

Reads services/*/api/openapi.yaml with PyYAML (skipped when it is not installed; `pip install pyyaml`) and applies the
gate of api_reference.py: operationId, summary or description, x-passage-duty, a 2xx response, every response described.
A spec that is not valid YAML (an unquoted ": " in a description, for example) fails here, because the reference build
parses the same file.

supplier-portal-ts is the one open exception: it documents the vendor portal BFF and carries neither operationId nor
x-passage-duty on its operations; which duty guards each BFF route is the portal owner's call (see docs/ops/NOT-IMPLEMENTED.md).

Run: python3 -m unittest discover -s docs/ops/tools
"""

import unittest
from pathlib import Path

import api_reference as ar

try:
    import yaml
except ImportError:  # pragma: no cover
    yaml = None

ROOT = Path(__file__).resolve().parents[3]
EXCEPTIONS = {"supplier-portal-ts"}


@unittest.skipIf(yaml is None, "PyYAML is not installed")
class TestEveryServiceSpecIsDocumented(unittest.TestCase):
    def test_every_spec_parses_and_documents_every_operation(self):
        specs = sorted((ROOT / "services").glob("*/api/openapi.yaml"))
        self.assertGreater(len(specs), 50)
        problems = []
        for path in specs:
            service = path.parts[-3]
            if service in EXCEPTIONS:
                continue
            try:
                doc = yaml.safe_load(path.read_text(encoding="utf-8"))
            except yaml.YAMLError as exc:
                problems.append(f"{service}: not valid YAML: {exc.problem} (line {exc.problem_mark.line + 1})")
                continue
            for op in ar.operations(doc):
                problems.extend(f"{service}: {op.method.upper()} {op.path}: {gap}" for gap in ar.gaps(op, doc))
        self.assertEqual(problems, [])


if __name__ == "__main__":
    unittest.main()
