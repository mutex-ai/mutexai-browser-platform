#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME="${PLATFORM_CLUSTER_NAME:-mutexai-dev}"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="${PLATFORM_NAMESPACE:-mutexai-platform}"
RELEASE="browser-platform"

info() { printf '[INFO] %s\n' "$1"; }
fail() { printf '[ERROR] %s\n' "$1" >&2; exit 1; }

for cmd in helm kubectl; do
    command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is not installed"
done

kubectl --context "$KUBE_CONTEXT" get nodes --request-timeout=10s >/dev/null || \
    fail "Cannot reach '$KUBE_CONTEXT'. No release was changed."

info "Uninstalling $RELEASE from $KUBE_CONTEXT / $NAMESPACE"
helm uninstall "$RELEASE" --kube-context "$KUBE_CONTEXT" \
    --namespace "$NAMESPACE" --ignore-not-found --wait --timeout=2m

info "Application and chart-created Secrets uninstalled. PostgreSQL, externally managed Secrets and the namespace are retained."
info "Private values are retained locally for reinstalling with the same database credentials."
info "To reinstall: make install"
