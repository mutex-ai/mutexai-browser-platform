#!/usr/bin/env python3
"""Check secret delivery and private deployment without real credentials."""
import json
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
    result = render(local, path)
    assert result.returncode == 0, result.stderr
    parts = documents(result.stdout)
    secret = next(p for p in parts if re.search(r"^kind: Secret$", p, re.M))
    deployment = next(p for p in parts if re.search(r"^kind: Deployment$", p, re.M))
    for key, value in [("POSTGRES_PASSWORD", "chart-test-password"), ("KEYCLOAK_CLIENT_SECRET", "keycloak-test-secret")]:
        assert f'{key}: "{value}"' in secret
        assert value not in deployment
    assert 'name: "browser-platform-secrets"' in deployment
    assert "checksum/secrets:" in deployment
    assert "type: ClusterIP" in result.stdout and "ingress: []" in result.stdout
    assert not any(re.search(r"^kind: Ingress$", p, re.M) for p in parts)
    subprocess.run(["helm", "lint", str(chart), "--values", str(path)], check=True)

    external = {"existingSecret": "platform-from-vault", "postgres": {"host": "postgres.production"}, "keycloak": {"url": "https://keycloak.production"}}
    result = render(external, path)
    assert result.returncode == 0, result.stderr
    assert not any(re.search(r"^kind: Secret$", p, re.M) for p in documents(result.stdout))
    assert 'name: "platform-from-vault"' in result.stdout
    assert 'value: "verify-full"' in result.stdout
    subprocess.run(["helm", "lint", str(chart), "--values", str(path)], check=True)
    for section, field in [("postgres", "password"), ("keycloak", "clientSecret"), ("keycloak", "url")]:
        values = json.loads(json.dumps(local))
        values[section][field] = ""
        result = render(values, path)
        assert result.returncode != 0 and f"{section}.{field}" in result.stderr
    assert "CONSOLE_URL" not in deployment and "CONSOLE_CLIENT_SECRET" not in deployment
print("Chart checks passed: provisioner credentials, existingSecret, private service and required configuration.")
