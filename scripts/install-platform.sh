#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CLUSTER_NAME="${PLATFORM_CLUSTER_NAME:-mutexai-dev}"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
NAMESPACE="${PLATFORM_NAMESPACE:-mutexai-platform}"
RELEASE="browser-platform"
VALUES_FILE="${PLATFORM_VALUES_FILE:-${REPO_ROOT}/.local/values.yaml}"
IMAGE_NAME="mutexai-browser-platform"
IMAGE_TAG="dev-$(date +%Y%m%d%H%M%S)-$$"

info() { printf '[INFO] %s\n' "$1"; }
fail() { printf '[ERROR] %s\n' "$1" >&2; exit 1; }

for cmd in helm docker kubectl kind; do
    command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is not installed"
done

[[ -f "$VALUES_FILE" ]] || fail "Create .local/values.yaml from charts/browser-platform/values-local.example.yaml, or set PLATFORM_VALUES_FILE. Supply connection settings and each dependency's private credential or existingSecret."

# Validate the deployment values without printing Secrets or putting passwords
# in command-line flags.
helm template "$RELEASE" "${REPO_ROOT}/charts/browser-platform" \
    --namespace "$NAMESPACE" --values "$VALUES_FILE" >/dev/null

kind get clusters 2>/dev/null | grep -Fxq -- "$CLUSTER_NAME" || \
    fail "Kind cluster '$CLUSTER_NAME' was not found. Use the shared Mutex local cluster."

# Never depend on, or change, the user's current kubectl context.
kubectl --context "$KUBE_CONTEXT" get nodes --request-timeout=10s >/dev/null || \
    fail "Cannot reach '$KUBE_CONTEXT'. Check its kubeconfig and API-server port before installing."

kubectl --context "$KUBE_CONTEXT" get namespace "$NAMESPACE" >/dev/null 2>&1 || \
    kubectl --context "$KUBE_CONTEXT" create namespace "$NAMESPACE"

info "Building ${IMAGE_NAME}:${IMAGE_TAG}"
docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" "$REPO_ROOT"

info "Loading the image into $CLUSTER_NAME"
kind load docker-image "${IMAGE_NAME}:${IMAGE_TAG}" --name "$CLUSTER_NAME"

info "Installing $RELEASE in $KUBE_CONTEXT / $NAMESPACE"
case "$(helm version --short)" in
    v4.*) rollback_flag="--rollback-on-failure" ;;
    *) rollback_flag="--atomic" ;;
esac
helm upgrade --install "$RELEASE" "${REPO_ROOT}/charts/browser-platform" \
    --kube-context "$KUBE_CONTEXT" \
    --namespace "$NAMESPACE" \
    --values "$VALUES_FILE" \
    --set-string image.registry="" \
    --set-string image.repository="$IMAGE_NAME" \
    --set-string image.tag="$IMAGE_TAG" \
    --set image.pullPolicy=Never \
    "$rollback_flag" --wait --timeout=5m

kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" \
    rollout status "deployment/$RELEASE" --timeout=60s

info "Installed ${IMAGE_NAME}:${IMAGE_TAG}"
info "kubectl --context $KUBE_CONTEXT -n $NAMESPACE port-forward deployment/$RELEASE 8002:8080"
info "curl http://127.0.0.1:8002/internal/v1/health/ready"
