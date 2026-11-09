import json
import pathlib

import jsonschema
import pytest

import aura_worker

SCHEMAS = pathlib.Path(__file__).resolve().parents[3] / "contracts" / "schemas"


def test_package_imports():
    assert aura_worker.PHASE == "P1"


@pytest.mark.parametrize("path", sorted(SCHEMAS.glob("*.json")), ids=lambda p: p.name)
def test_contract_schema_is_valid_draft_2020_12(path):
    schema = json.loads(path.read_text())
    jsonschema.Draft202012Validator.check_schema(schema)
    assert schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
