"""Bicycle 50-constraint fixture shared with the Go domain tests."""

from __future__ import annotations

from pathlib import Path

import yaml

FIXTURE = Path(__file__).resolve().parents[2] / "testdata" / "configurator-50.yaml"


def load_fixture(path: Path | None = None) -> dict:
    with (path or FIXTURE).open() as f:
        return yaml.safe_load(f)


def domains(model: dict) -> dict[str, list[str]]:
    return {a["name"]: list(a["domain"]) for a in model["attributes"]}
