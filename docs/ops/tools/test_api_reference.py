"""Tests for api_reference.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import json
import tempfile
import unittest
from pathlib import Path

import api_reference as ar


def documented_op(**extra):
    op = {
        "operationId": "listThings",
        "summary": "List things",
        "description": "Lists things the caller may read.",
        "x-passage-duty": "thing.read",
        "responses": {"200": {"description": "ok"}},
    }
    op.update(extra)
    return op


class TestPublicRoutes(unittest.TestCase):
    def test_x_passage_public_stands_in_for_the_duty(self):
        op = documented_op()
        del op["x-passage-duty"]
        doc = {"paths": {"/a": {"post": dict(op, **{"x-passage-public": True})}, "/b": {"post": dict(op)}}}
        by_path = {o.path: ar.gaps(o, doc) for o in ar.operations(doc)}
        self.assertEqual(by_path["/a"], [])
        self.assertEqual(by_path["/b"], ["POST /b: missing x-passage-duty"])


class TestOperations(unittest.TestCase):
    def test_ordered_by_path_then_method_and_skips_non_operations(self):
        doc = {
            "paths": {
                "/b": {"get": documented_op()},
                "/a": {"post": documented_op(), "parameters": [], "get": documented_op()},
            }
        }
        got = [(o.method, o.path) for o in ar.operations(doc)]
        self.assertEqual(got, [("get", "/a"), ("post", "/a"), ("get", "/b")])

    def test_non_dict_path_item_is_ignored(self):
        self.assertEqual(ar.operations({"paths": {"/x": None}}), [])

    def test_probe_and_internal_routes_are_not_published(self):
        doc = {
            "paths": {
                "/healthz": {"get": {"responses": {"200": {}}}},
                "/readyz": {"get": {"responses": {"200": {}}}},
                "/metrics": {"get": {"responses": {"200": {}}}},
                "/internal/v1/inventory/onhand:rebuild": {"post": {"responses": {}}},
                "/v1/x/things": {"get": documented_op()},
            }
        }
        self.assertEqual([o.path for o in ar.operations(doc)], ["/v1/x/things"])


class TestGaps(unittest.TestCase):
    def test_fully_documented_operation_has_no_gaps(self):
        doc = {"paths": {"/a": {"get": documented_op()}}}
        (o,) = ar.operations(doc)
        self.assertEqual(ar.gaps(o, doc), [])

    def test_missing_fields_are_reported_each(self):
        doc = {"paths": {"/a": {"post": {"responses": {"400": {"description": "bad"}}}}}}
        (o,) = ar.operations(doc)
        problems = ar.gaps(o, doc)
        self.assertEqual(
            problems,
            [
                "POST /a: missing operationId",
                "POST /a: missing summary or description",
                "POST /a: missing x-passage-duty",
                "POST /a: no 2xx response",
            ],
        )

    def test_summary_or_description_is_enough(self):
        for op in (documented_op(description=""), documented_op(summary="")):
            with self.subTest(op=op):
                doc = {"paths": {"/a": {"get": op}}}
                (o,) = ar.operations(doc)
                self.assertEqual(ar.gaps(o, doc), [])

    def test_blank_summary_and_description_count_as_missing(self):
        doc = {"paths": {"/a": {"get": documented_op(summary="   ", description=None)}}}
        (o,) = ar.operations(doc)
        self.assertEqual(ar.gaps(o, doc), ["GET /a: missing summary or description"])

    def test_response_without_description_is_reported(self):
        doc = {"paths": {"/a": {"get": documented_op(responses={"200": {}})}}}
        (o,) = ar.operations(doc)
        self.assertEqual(ar.gaps(o, doc), ["GET /a: response 200 missing description"])

    def test_response_ref_resolves_before_checking(self):
        doc = {
            "components": {"responses": {"Ok": {"description": "shared ok"}}},
            "paths": {"/a": {"get": documented_op(responses={"200": {"$ref": "#/components/responses/Ok"}})}},
        }
        (o,) = ar.operations(doc)
        self.assertEqual(ar.gaps(o, doc), [])

    def test_unresolvable_ref_raises(self):
        doc = {"paths": {"/a": {"get": documented_op(responses={"200": {"$ref": "#/components/responses/Nope"}})}}}
        (o,) = ar.operations(doc)
        with self.assertRaises(ValueError):
            ar.gaps(o, doc)


class TestRender(unittest.TestCase):
    def test_table_and_sections_present(self):
        doc = {
            "openapi": "3.1.0",
            "info": {"version": "1.2.0"},
            "paths": {
                "/api/v1/x/things": {
                    "post": documented_op(
                        summary="Create | thing",
                        parameters=[{"name": "Idempotency-Key", "in": "header", "required": True}],
                        responses={"201": {"description": "created"}, "400": {"description": "bad"}},
                    )
                }
            },
        }
        text = ar.render("x", doc)
        self.assertIn("# x API reference", text)
        self.assertIn("API version 1.2.0", text)
        self.assertIn("1 operations.", text)
        self.assertIn("| POST | /api/v1/x/things | listThings | Create \\| thing | thing.read | yes | 201 |", text)
        self.assertIn("- Operation: `listThings`", text)
        self.assertIn("## POST /api/v1/x/things", text)
        self.assertIn("- `Idempotency-Key` (header, required)", text)
        self.assertIn("- `201`: created", text)

    def test_table_falls_back_to_the_first_sentence_of_the_description(self):
        doc = {"paths": {"/a": {"get": documented_op(summary="", description="Reads a thing. Then more.")}}}
        self.assertIn("| GET | /a | listThings | Reads a thing | thing.read | no | 200 |", ar.render("s", doc))

    def test_render_is_deterministic(self):
        doc = {"paths": {"/a": {"get": documented_op()}, "/b": {"get": documented_op()}}}
        self.assertEqual(ar.render("s", doc), ar.render("s", doc))


class TestMain(unittest.TestCase):
    def _write(self, directory: Path, name: str, doc: dict) -> Path:
        path = directory / name
        path.write_text(json.dumps(doc), encoding="utf-8")
        return path

    def test_passes_and_writes_pages(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            spec = self._write(root, "org.json", {"openapi": "3.1.0", "paths": {"/a": {"get": documented_op()}}})
            out = root / "pages"
            self.assertEqual(ar.main(["--spec", str(spec), "--out", str(out)]), 0)
            self.assertTrue((out / "org.md").is_file())

    def test_service_openapi_file_is_named_after_its_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "pages"
            specs = []
            for svc in ("org", "ledger"):
                (Path(tmp) / svc / "api").mkdir(parents=True)
                specs += ["--spec", str(self._write(Path(tmp) / svc / "api", "openapi.json", {"paths": {"/a": {"get": documented_op()}}}))]
            self.assertEqual(ar.main(specs + ["--out", str(out)]), 0)
            self.assertEqual(sorted(p.name for p in out.iterdir()), ["ledger.md", "org.md"])
            self.assertIn("# org API reference", (out / "org.md").read_text(encoding="utf-8"))

    def test_two_specs_with_one_name_are_a_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            a = Path(tmp) / "a"
            b = Path(tmp) / "b"
            a.mkdir()
            b.mkdir()
            doc = {"paths": {"/a": {"get": documented_op()}}}
            args = ["--spec", str(self._write(a, "org.json", doc)), "--spec", str(self._write(b, "org.json", doc))]
            self.assertEqual(ar.main(args), 2)

    def test_fails_on_undocumented_operation(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = self._write(Path(tmp), "bad.json", {"paths": {"/a": {"get": {"responses": {}}}}})
            self.assertEqual(ar.main(["--spec", str(spec)]), 1)

    def test_yaml_input_is_a_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = Path(tmp) / "openapi.yaml"
            spec.write_text("paths: {}\n", encoding="utf-8")
            self.assertEqual(ar.main(["--spec", str(spec)]), 2)

    def test_not_an_openapi_document_is_a_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = self._write(Path(tmp), "junk.json", {"hello": "world"})
            self.assertEqual(ar.main(["--spec", str(spec)]), 2)


if __name__ == "__main__":
    unittest.main()
