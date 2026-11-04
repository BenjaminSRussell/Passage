import json
import sys
from pathlib import Path

import yaml


def to_json_schema(node):
    if isinstance(node, dict):
        out = {k: to_json_schema(v) for k, v in node.items() if not k.startswith("x-kubernetes-")}
        if node.get("x-kubernetes-int-or-string"):
            out["type"] = ["integer", "string"]
        return out
    if isinstance(node, list):
        return [to_json_schema(v) for v in node]
    return node


def main(crds_file, out_dir):
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    for crd in yaml.safe_load_all(Path(crds_file).read_text()):
        if not crd or crd["kind"] != "CustomResourceDefinition":
            continue
        spec = crd["spec"]
        for version in spec["versions"]:
            schema = to_json_schema(version["schema"]["openAPIV3Schema"])
            # kubeconform v0.6.7 lowercases {{.ResourceKind}} when expanding
            # -schema-location; PascalCase names pass on macOS and fail on GHA.
            name = f"{spec['names']['kind'].lower()}-{spec['group']}-{version['name']}.json"
            (out / name).write_text(json.dumps(schema))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
