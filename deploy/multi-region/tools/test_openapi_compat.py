"""Tests for openapi_compat.py (PSG-50). Run: python3 -m unittest discover -s deploy/multi-region/tools"""

import copy
import unittest
from typing import Any

import openapi_compat as oc


def doc(paths: dict[str, Any], schemas: dict[str, Any] | None = None) -> dict[str, Any]:
    d: dict[str, Any] = {"openapi": "3.1.0", "paths": paths}
    if schemas is not None:
        d["components"] = {"schemas": schemas}
    return d


def body_op(schema: dict[str, Any], required: bool = False) -> dict[str, Any]:
    return {"requestBody": {"required": required, "content": {"application/json": {"schema": schema}}}}


def reply(schema: dict[str, Any], code: str = "200") -> dict[str, Any]:
    return {"responses": {code: {"description": "ok", "content": {"application/json": {"schema": schema}}}}}


ENTITY = {
    "type": "object",
    "required": ["id", "name"],
    "properties": {
        "id": {"type": "string"},
        "name": {"type": "string"},
        "status": {"type": "string", "enum": ["active", "inactive"]},
    },
}


def base() -> dict[str, Any]:
    # Fresh copies every time: tests mutate the returned documents, and ENTITY must stay pristine.
    return doc(
        {
            "/v1/party/parties": {
                "post": {**body_op({"type": "object", "properties": {"name": {"type": "string"}}}), **reply(copy.deepcopy(ENTITY), "201")},
                "get": {
                    "parameters": [{"name": "limit", "in": "query", "schema": {"type": "integer"}}],
                    **reply({"type": "object", "properties": {"items": {"type": "array", "items": {"$ref": "#/components/schemas/Entity"}}}}),
                },
            },
            "/v1/party/parties/{id}": {
                "get": {
                    "parameters": [{"name": "id", "in": "path", "required": True, "schema": {"type": "string"}}],
                    **reply({"$ref": "#/components/schemas/Entity"}),
                },
            },
        },
        {"Entity": copy.deepcopy(ENTITY)},
    )


def kinds(changes: list[oc.Change]) -> list[str]:
    return [c.kind for c in changes]


class TestAdditiveIsCompatible(unittest.TestCase):
    def test_identical_documents(self) -> None:
        self.assertEqual(oc.compare(base(), base()), [])

    def test_new_path_and_method_allowed(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties/{id}"]["delete"] = {"responses": {"204": {"description": "gone"}}}
        new["paths"]["/v1/party/merges"] = {"post": reply(ENTITY, "201")}
        self.assertEqual(oc.compare(base(), new), [])

    def test_optional_parameter_and_new_status_allowed(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["get"]["parameters"].append({"name": "cursor", "in": "query", "schema": {"type": "string"}})
        new["paths"]["/v1/party/parties"]["post"]["responses"]["202"] = {"description": "queued"}
        self.assertEqual(oc.compare(base(), new), [])

    def test_optional_request_and_response_fields_allowed(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["email"] = {"type": "string"}
        new["components"]["schemas"]["Entity"]["properties"]["note"] = {"type": "string"}
        self.assertEqual(oc.compare(base(), new), [])

    def test_relaxed_request_required_is_allowed(self) -> None:
        old = base()
        old["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]["required"] = ["name"]
        self.assertEqual(oc.compare(old, base()), [])


class TestBreakingChanges(unittest.TestCase):
    def test_path_removed(self) -> None:
        new = base()
        del new["paths"]["/v1/party/parties/{id}"]
        self.assertEqual(kinds(oc.compare(base(), new)), ["path-removed"])

    def test_method_removed(self) -> None:
        new = base()
        del new["paths"]["/v1/party/parties"]["get"]
        changes = oc.compare(base(), new)
        self.assertEqual(kinds(changes), ["operation-removed"])
        self.assertEqual(changes[0].location, "GET /v1/party/parties")

    def test_new_required_parameter(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["get"]["parameters"].append({"name": "tenantScope", "in": "query", "required": True, "schema": {"type": "string"}})
        self.assertEqual(kinds(oc.compare(base(), new)), ["parameter-required"])

    def test_parameter_became_required(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["get"]["parameters"][0]["required"] = True
        self.assertEqual(kinds(oc.compare(base(), new)), ["parameter-required"])

    def test_parameter_removed(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["get"]["parameters"] = []
        self.assertEqual(kinds(oc.compare(base(), new)), ["parameter-removed"])

    def test_new_required_request_field(self) -> None:
        new = base()
        s = new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        s["properties"]["taxId"] = {"type": "string"}
        s["required"] = ["taxId"]
        self.assertEqual(kinds(oc.compare(base(), new)), ["request-field-required"])

    def test_request_field_became_required(self) -> None:
        new = base()
        s = new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        s["required"] = ["name"]
        self.assertEqual(kinds(oc.compare(base(), new)), ["request-field-required"])

    def test_request_field_removed(self) -> None:
        new = base()
        s = new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        s["properties"] = {}
        self.assertEqual(kinds(oc.compare(base(), new)), ["request-field-removed"])

    def test_request_body_became_required(self) -> None:
        old = base()
        new = copy.deepcopy(base())
        new["paths"]["/v1/party/parties"]["post"]["requestBody"]["required"] = True
        self.assertEqual(kinds(oc.compare(old, new)), ["request-body-required"])

    def test_response_status_removed(self) -> None:
        new = base()
        del new["paths"]["/v1/party/parties"]["post"]["responses"]["201"]
        self.assertEqual(kinds(oc.compare(base(), new)), ["response-removed"])

    def test_response_field_removed_through_ref(self) -> None:
        new = base()
        del new["components"]["schemas"]["Entity"]["properties"]["name"]
        changes = oc.compare(base(), new)
        self.assertIn("response-field-removed", kinds(changes))
        self.assertTrue(any(c.location.endswith(".name") for c in changes))

    def test_response_field_removed_inside_array_items(self) -> None:
        new = base()
        new["components"]["schemas"]["Entity"]["properties"].pop("status")
        locs = [c.location for c in oc.compare(base(), new) if c.kind == "response-field-removed"]
        self.assertIn("GET /v1/party/parties 200.items[].status", locs)

    def test_type_changed(self) -> None:
        new = base()
        new["components"]["schemas"]["Entity"]["properties"]["id"]["type"] = "integer"
        self.assertIn("type-changed", kinds(oc.compare(base(), new)))

    def test_enum_value_removed_from_request(self) -> None:
        new = base()
        s = new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        s["properties"]["kind"] = {"type": "string", "enum": ["person"]}
        old = base()
        old["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["kind"] = {
            "type": "string",
            "enum": ["person", "organization"],
        }
        changes = oc.compare(old, new)
        self.assertEqual(kinds(changes), ["enum-value-removed"])
        self.assertEqual(changes[0].detail, '"organization"')

    def test_enum_added_to_a_free_request_field(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["name"]["enum"] = ["a"]
        self.assertEqual(kinds(oc.compare(base(), new)), ["enum-value-removed"])

    def test_response_field_no_longer_required(self) -> None:
        new = base()
        new["components"]["schemas"]["Entity"]["required"] = ["id"]
        changes = oc.compare(base(), new)
        self.assertEqual(set(kinds(changes)), {"response-field-optional"})
        self.assertIn("GET /v1/party/parties/{id} 200.name", [c.location for c in changes])

    def test_response_may_return_null(self) -> None:
        new = base()
        new["components"]["schemas"]["Entity"]["properties"]["name"]["type"] = ["string", "null"]
        self.assertEqual(set(kinds(oc.compare(base(), new))), {"type-changed"})

    def test_response_nullable_30_style_is_a_new_type(self) -> None:
        new = base()
        new["components"]["schemas"]["Entity"]["properties"]["name"]["nullable"] = True
        self.assertEqual(set(kinds(oc.compare(base(), new))), {"type-changed"})

    def test_request_no_longer_accepts_null(self) -> None:
        old = base()
        old["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["name"]["type"] = ["null", "string"]
        self.assertEqual(kinds(oc.compare(old, base())), ["type-changed"])

    def test_request_closed_to_unknown_fields(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]["additionalProperties"] = False
        self.assertEqual(kinds(oc.compare(base(), new)), ["request-closed"])

    def test_parameter_type_narrowed(self) -> None:
        old = base()
        old["paths"]["/v1/party/parties"]["get"]["parameters"][0]["schema"] = {"type": "number"}
        changes = oc.compare(old, base())
        self.assertEqual(kinds(changes), ["type-changed"])
        self.assertEqual(changes[0].location, "GET /v1/party/parties query limit")

    def test_request_media_type_removed(self) -> None:
        old = base()
        old["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["text/csv"] = {"schema": {"type": "string"}}
        self.assertEqual(kinds(oc.compare(old, base())), ["request-media-removed"])

    def test_response_json_body_removed(self) -> None:
        new = base()
        new["paths"]["/v1/party/parties/{id}"]["get"]["responses"]["200"] = {"description": "ok"}
        self.assertEqual(kinds(oc.compare(base(), new)), ["response-media-removed"])

    def test_field_removed_from_an_allof_member(self) -> None:
        def with_detail(entity_props: dict[str, Any]) -> dict[str, Any]:
            d = base()
            d["components"]["schemas"]["Entity"]["properties"] = entity_props
            d["components"]["schemas"]["Detail"] = {
                "allOf": [{"$ref": "#/components/schemas/Entity"}, {"type": "object", "properties": {"lines": {"type": "array"}}}]
            }
            d["paths"]["/v1/party/parties/{id}"]["get"]["responses"]["200"]["content"]["application/json"]["schema"] = {
                "$ref": "#/components/schemas/Detail"
            }
            return d

        old = with_detail(copy.deepcopy(ENTITY["properties"]))
        props = copy.deepcopy(ENTITY["properties"])
        del props["status"]
        locs = [c.location for c in oc.compare(old, with_detail(props))]
        self.assertIn("GET /v1/party/parties/{id} 200.status", locs)
        self.assertEqual(oc.compare(old, copy.deepcopy(old)), [])


class TestDirection(unittest.TestCase):
    def test_request_may_widen(self) -> None:
        new = base()
        s = new["paths"]["/v1/party/parties"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        s["properties"]["name"]["type"] = ["string", "null"]
        new["paths"]["/v1/party/parties"]["get"]["parameters"][0]["schema"]["type"] = "number"
        self.assertEqual(oc.compare(base(), new), [])

    def test_response_may_narrow_and_drop_enum_values(self) -> None:
        old = base()
        old["components"]["schemas"]["Entity"]["properties"]["name"]["type"] = ["string", "null"]
        new = base()
        new["components"]["schemas"]["Entity"]["properties"]["status"]["enum"] = ["active"]
        self.assertEqual(oc.compare(old, new), [])

    def test_new_response_enum_value_is_a_warning_only(self) -> None:
        new = base()
        new["components"]["schemas"]["Entity"]["properties"]["status"]["enum"].append("merged")
        report = oc.check(base(), new)
        self.assertEqual(report.breaking, [])
        self.assertEqual({c.kind for c in report.warnings}, {"enum-value-added"})
        self.assertTrue(all(c.detail == '"merged"' for c in report.warnings))

    def test_renamed_path_parameter_is_not_a_change(self) -> None:
        new = base()
        item = new["paths"].pop("/v1/party/parties/{id}")
        item["get"]["parameters"][0]["name"] = "partyId"
        new["paths"]["/v1/party/parties/{partyId}"] = item
        self.assertEqual(oc.compare(base(), new), [])


class TestRecursionAndErrors(unittest.TestCase):
    def test_self_referencing_schema_terminates(self) -> None:
        node = {"type": "object", "properties": {"id": {"type": "string"}, "child": {"$ref": "#/components/schemas/Node"}}}
        old = doc({"/v1/x": {"get": reply({"$ref": "#/components/schemas/Node"})}}, {"Node": node})
        self.assertEqual(oc.compare(old, copy.deepcopy(old)), [])

    def test_external_ref_is_an_error(self) -> None:
        d = doc({"/v1/x": {"get": reply({"$ref": "other.json#/Thing"})}})
        with self.assertRaises(ValueError):
            oc.compare(d, d)

    def test_unresolved_ref_is_an_error(self) -> None:
        d = doc({"/v1/x": {"get": reply({"$ref": "#/components/schemas/Missing"})}})
        with self.assertRaises(ValueError):
            oc.compare(d, d)

    def test_main_exit_codes(self) -> None:
        import contextlib
        import io
        import json
        import tempfile
        from pathlib import Path

        new = base()
        del new["paths"]["/v1/party/parties/{id}"]
        with tempfile.TemporaryDirectory() as tmp:
            o, n, bad = Path(tmp) / "o.json", Path(tmp) / "n.json", Path(tmp) / "bad.json"
            o.write_text(json.dumps(base()), encoding="utf-8")
            n.write_text(json.dumps(new), encoding="utf-8")
            bad.write_text("{not json", encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(oc.main(["--old", str(o), "--new", str(o)]), 0)
                self.assertEqual(oc.main(["--old", str(o), "--new", str(n)]), 1)
                self.assertEqual(oc.main(["--old", str(o), "--new", str(bad)]), 2)

    def test_load_doc_rejects_yaml_and_non_openapi(self) -> None:
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            y = Path(tmp) / "a.yaml"
            y.write_text("paths: {}", encoding="utf-8")
            with self.assertRaises(ValueError):
                oc.load_doc(y)
            j = Path(tmp) / "b.json"
            j.write_text('{"info": {}}', encoding="utf-8")
            with self.assertRaises(ValueError):
                oc.load_doc(j)


if __name__ == "__main__":
    unittest.main()
