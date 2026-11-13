#!/usr/bin/env python3
"""Check local and externally managed Secret modes without real credentials."""
import json
from pathlib import Path
import re
import subprocess
import tempfile

root = Path(__file__).resolve().parent.parent
chart = root / "charts/browser-platform"

def render(values, values_file):
    values_file.write_text(json.dumps(values))
    return subprocess.run(["helm", "template", "browser-platform", str(chart),
                           "--values", str(values_file)], text=True, capture_output=True)

def documents(manifest):
    return [part for part in manifest.split("---") if re.search(r"^kind:", part, re.M)]

with tempfile.TemporaryDirectory(prefix="platform-chart-check-") as directory:
    values_file = Path(directory) / "values.json"
    local = {"postgres": {"host": "postgres.local", "password": "chart-test-password", "sslMode": "disable"}}
    result = render(local, values_file)
    assert result.returncode == 0, result.stderr
    parts = documents(result.stdout)
    secret = next(part for part in parts if re.search(r"^kind: Secret$", part, re.M))
    deployment = next(part for part in parts if re.search(r"^kind: Deployment$", part, re.M))
    assert 'POSTGRES_PASSWORD: "chart-test-password"' in secret
    assert 'name: "browser-platform-secrets"' in deployment
    assert "chart-test-password" not in deployment
    assert "checksum/secrets:" in deployment
    assert "DATABASE_URL" not in result.stdout
    assert "type: ClusterIP" in result.stdout and "ingress: []" in result.stdout
    assert not any(re.search(r"^kind: Ingress$", part, re.M) for part in parts)
    subprocess.run(["helm", "lint", str(chart), "--values", str(values_file)], check=True)

    external = {"existingSecret": "platform-from-vault", "postgres": {"host": "postgres.production"}}
    result = render(external, values_file)
    assert result.returncode == 0, result.stderr
    assert not any(re.search(r"^kind: Secret$", part, re.M) for part in documents(result.stdout))
    assert 'name: "platform-from-vault"' in result.stdout
    assert 'value: "verify-full"' in result.stdout
    subprocess.run(["helm", "lint", str(chart), "--values", str(values_file)], check=True)

    result = render({"postgres": {"host": "postgres.local"}}, values_file)
    assert result.returncode != 0 and "postgres.password" in result.stderr

print("Chart checks passed: local Secret, existingSecret, private service and required credentials.")
