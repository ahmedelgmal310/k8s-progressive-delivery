#!/usr/bin/env bash
# Installs the e2e platform into the current kube context:
# Prometheus (lean) and Argo Rollouts, then waits until both are usable.
set -euo pipefail

PROMETHEUS_CHART_VERSION="${PROMETHEUS_CHART_VERSION:-29.35.0}"
ROLLOUTS_CHART_VERSION="${ROLLOUTS_CHART_VERSION:-2.43.6}"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
helm repo add argo https://argoproj.github.io/argo-helm --force-update >/dev/null
helm repo update >/dev/null

log "installing Argo Rollouts chart ${ROLLOUTS_CHART_VERSION}"
helm upgrade --install argo-rollouts argo/argo-rollouts \
  --version "$ROLLOUTS_CHART_VERSION" \
  --namespace argo-rollouts --create-namespace \
  -f e2e/rollouts-values.yaml --wait --timeout 5m

log "installing Prometheus chart ${PROMETHEUS_CHART_VERSION}"
helm upgrade --install prometheus prometheus-community/prometheus \
  --version "$PROMETHEUS_CHART_VERSION" \
  --namespace monitoring --create-namespace \
  -f e2e/prometheus-values.yaml --wait --timeout 5m

# CRDs must be established before Rollout objects are applied.
for crd in rollouts.argoproj.io analysistemplates.argoproj.io analysisruns.argoproj.io; do
  kubectl wait --for=condition=Established "crd/${crd}" --timeout=120s
done
kubectl -n argo-rollouts rollout status deploy/argo-rollouts --timeout=180s

# Prometheus must answer queries through the API server proxy used by run.sh.
for _ in $(seq 1 60); do
  if kubectl get --raw "/api/v1/namespaces/monitoring/services/prometheus-server:80/proxy/-/ready" >/dev/null 2>&1; then
    log "Prometheus ready"
    exit 0
  fi
  sleep 3
done
log "Prometheus did not become ready"
exit 1
