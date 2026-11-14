#!/usr/bin/env python3
"""Check secret delivery and private deployment without real credentials."""
import json
from itertools import product
from pathlib import Path
import re
import subprocess
import tempfile

chart = Path(__file__).resolve().parent.parent / "charts/browser-platform"


def render(values, path):
    path.write_text(json.dumps(values))
    return subprocess.run(["helm", "template", "browser-platform", str(chart), "--values", str(path)], text=True, capture_output=True)


def documents(manifest):
    return [part for part in manifest.split("---") if re.search(r"^kind:", part, re.M)]


with tempfile.TemporaryDirectory(prefix="platform-chart-check-") as directory:
    path = Path(directory) / "values.json"
    local = {
        "postgres": {"host": "postgres.local", "password": "chart-test-password", "sslMode": "disable"},
        "keycloak": {"url": "http://keycloak.local:8080", "clientSecret": "keycloak-test-secret"},
    }
    # Each dependency independently supports a chart-created or external Secret.
    credentials = [
        ("postgres", "password", "POSTGRES_PASSWORD"),
        ("keycloak", "clientSecret", "KEYCLOAK_CLIENT_SECRET"),
    ]
    for external_modes in product((False, True), repeat=2):
        values = json.loads(json.dumps(local))
        for (section, field, _), external in zip(credentials, external_modes):
            if external:
                values[section]['existingSecret'] = f'{section}-from-vault'
                values[section].pop(field)
        result = render(values, path)
        assert result.returncode == 0, result.stderr
        parts = documents(result.stdout)
        secrets = [p for p in parts if re.search(r"^kind: Secret$", p, re.M)]
        deployment = next(p for p in parts if re.search(r"^kind: Deployment$", p, re.M))
        assert len(secrets) == external_modes.count(False)
        for (section, field, key), external in zip(credentials, external_modes):
            name = f'{section}-from-vault' if external else f'browser-platform-{section}'
            assert f'name: "{name}"' in deployment
            assert (f'checksum/{section}:' in deployment) == (not external)
            value = local[section][field]
            assert value not in deployment
            if external:
                assert not any(f'{key}:' in secret for secret in secrets)
            else:
                secret = next(secret for secret in secrets if f'{key}:' in secret)
                assert f'{key}: "{value}"' in secret
                assert all(f'{other_key}:' not in secret for _, _, other_key in credentials if other_key != key)
        assert "type: ClusterIP" in result.stdout and "ingress: []" in result.stdout
        assert not any(re.search(r"^kind: Ingress$", p, re.M) for p in parts)
        subprocess.run(["helm", "lint", str(chart), "--values", str(path)], check=True)

    # Referenced credentials take precedence over stale private values.
    external = json.loads(json.dumps(local))
    for section, _, _ in credentials:
        external[section]['existingSecret'] = f'{section}-from-vault'
    external['postgres'].pop('sslMode')
    result = render(external, path)
    assert result.returncode == 0, result.stderr
    assert 'value: "verify-full"' in result.stdout
    assert not any(local[section][field] in result.stdout for section, field, _ in credentials)
    for section, field in [("postgres", "password"), ("keycloak", "clientSecret"), ("keycloak", "url")]:
        values = json.loads(json.dumps(local))
        values[section][field] = ""
        result = render(values, path)
        assert result.returncode != 0 and section in result.stderr and field in result.stderr
    # Helm's schema catches invalid values before a pod or Secret is submitted.
    invalid = [
        ({"replicaCount": -1}, "replicaCount"),
        ({"replicaCount": "1"}, "replicaCount"),
        ({"server": {"readTimeoutSec": 0}}, "readTimeoutSec"),
        ({"server": {"shutdownTimeoutSec": 3601}}, "shutdownTimeoutSec"),
        ({"server": {"writeTimeoutSec": "30"}}, "writeTimeoutSec"),
        ({"server": {"listenAddr": ":9000"}}, "listenAddr"),
        ({"postgres": {"port": 65536}}, "port"),
        ({"postgres": {"database": ""}}, "database"),
        ({"postgres": {"username": " "}}, "username"),
        ({"postgres": {"sslMode": "optional"}}, "sslMode"),
        ({"keycloak": {"clientId": ""}}, "clientId"),
        ({"keycloak": {"url": "ftp://keycloak.local"}}, "url"),
        ({"image": {"pullPolicy": "Sometimes"}}, "pullPolicy"),
        ({"imagePullSecrets": ["acr-pull"]}, "imagePullSecrets"),
        ({"existingSecret": True}, "existingSecret"),
        ({"postgres": {"existingSecret": True}}, "existingSecret"),
        ({"keycloak": {"existingSecret": True}}, "existingSecret"),
        ({"consoleSecrets": {"LOCAL": "retired-setting"}}, "consoleSecrets"),
    ]
    for overrides, field in invalid:
        values = json.loads(json.dumps(local))
        for section, value in overrides.items():
            if isinstance(value, dict) and isinstance(values.get(section), dict):
                values[section].update(value)
            else:
                values[section] = value
        result = render(values, path)
        assert result.returncode != 0 and "schema" in result.stderr and field in result.stderr, field
    assert "CONSOLE_URL" not in deployment and "CONSOLE_CLIENT_SECRET" not in deployment
print("Chart checks passed: independent dependency Secrets, private service and native values validation.")
