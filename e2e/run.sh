#!/usr/bin/env bash
# End-to-end proof on a kind cluster:
#   v1 (good) initial deploy -> v2 (good) must be fully promoted by the canary
#   -> v3 (bad, 5xx) must fail analysis and be aborted, leaving v2 serving.
# Requires: kubectl, helm, jq, a running cluster with Prometheus + Argo Rollouts
# (see e2e/setup.sh) and the images demo-api:{v1,v2,v3} loaded into the nodes.
set -euo pipefail

NS="${NS:-demo}"
RELEASE="${RELEASE:-demo-api}"
CHART="${CHART:-charts/demo-api}"
IMAGE_REPO="${IMAGE_REPO:-demo-api}"
RESULTS_DIR="${RESULTS_DIR:-e2e-results}"
PROMOTE_TIMEOUT="${PROMOTE_TIMEOUT:-600}"
ROLLBACK_TIMEOUT="${ROLLBACK_TIMEOUT:-420}"
REPLICAS=10

mkdir -p "$RESULTS_DIR"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

dump() {
  log "---- diagnostics ----"
  kubectl -n "$NS" get rollout,replicaset,pods,analysisrun -o wide >&2 || true
  kubectl -n "$NS" get rollout "$RELEASE" -o jsonpath='{.status}' >&2 || true
  echo >&2
  kubectl -n "$NS" get analysisrun -o json \
    | jq '[.items[] | {name: .metadata.name, phase: .status.phase, message: .status.message,
           metrics: [.status.metricResults[]? | {name, phase, message,
             values: [.measurements[]? | .value]}]}]' >&2 || true
  kubectl -n loadtest logs job/k6-load --tail=20 >&2 || true
  kubectl -n argo-rollouts logs deploy/argo-rollouts --tail=40 >&2 || true
}

die() {
  log "FAIL: $*"
  dump
  exit 1
}

ro() { kubectl -n "$NS" get rollout "$RELEASE" -o "jsonpath=$1"; }

prom_query() {
  local q
  q=$(jq -rn --arg q "$1" '$q|@uri')
  kubectl get --raw "/api/v1/namespaces/monitoring/services/prometheus-server:80/proxy/api/v1/query?query=${q}" \
    | jq -r '.data.result[0].value[1] // "NaN"'
}

deploy() {
  log "helm upgrade --install ${RELEASE} image.tag=$1"
  helm upgrade --install "$RELEASE" "$CHART" -n "$NS" \
    -f e2e/values-e2e.yaml \
    --set image.repository="$IMAGE_REPO" \
    --set image.tag="$1" >/dev/null
}

# Wait for the controller to create the ReplicaSet for a new pod template.
wait_new_revision() {
  local prev=$1 deadline=$((SECONDS + 120)) h
  while ((SECONDS < deadline)); do
    h=$(ro '{.status.currentPodHash}')
    if [[ -n $h && $h != "$prev" ]]; then
      echo "$h"
      return 0
    fi
    sleep 2
  done
  die "controller did not observe a new revision (still ${prev})"
}

# Wait until the given pod hash is the stable ReplicaSet and the rollout is Healthy.
wait_promoted() {
  local hash=$1 timeout=$2 deadline phase stable abort last=""
  deadline=$((SECONDS + timeout))
  while ((SECONDS < deadline)); do
    phase=$(ro '{.status.phase}')
    stable=$(ro '{.status.stableRS}')
    abort=$(ro '{.status.abort}')
    if [[ "$phase/$(ro '{.status.currentStepIndex}')" != "$last" ]]; then
      last="$phase/$(ro '{.status.currentStepIndex}')"
      log "  phase=${phase} step=$(ro '{.status.currentStepIndex}') weight=$(ro '{.status.canary.weights.canary.weight}') stable=${stable}"
    fi
    if [[ $abort == "true" ]]; then
      die "rollout of ${hash} was aborted: $(ro '{.status.message}')"
    fi
    if [[ $phase == "Healthy" && $stable == "$hash" ]]; then
      return 0
    fi
    sleep 3
  done
  die "rollout of ${hash} was not promoted within ${timeout}s"
}

# Wait until the rollout is aborted and the canary scaled to zero.
wait_aborted() {
  local canary=$1 stable_hash=$2 timeout=$3 deadline abort phase last=""
  ABORT_AT=""
  deadline=$((SECONDS + timeout))
  while ((SECONDS < deadline)); do
    abort=$(ro '{.status.abort}')
    phase=$(ro '{.status.phase}')
    if [[ "$phase/$(ro '{.status.currentStepIndex}')" != "$last" ]]; then
      last="$phase/$(ro '{.status.currentStepIndex}')"
      log "  phase=${phase} step=$(ro '{.status.currentStepIndex}') abort=${abort:-false}"
    fi
    if [[ $(ro '{.status.stableRS}') == "$canary" ]]; then
      die "bad revision ${canary} was promoted to stable"
    fi
    if [[ $abort == "true" && -z $ABORT_AT ]]; then
      ABORT_AT=$SECONDS
      log "  rollout aborted: $(ro '{.status.message}')"
    fi
    if [[ -n $ABORT_AT ]]; then
      local canary_pods ready_stable
      canary_pods=$(kubectl -n "$NS" get pods -l "rollouts-pod-template-hash=${canary}" \
        --field-selector=status.phase!=Succeeded,status.phase!=Failed -o name | wc -l)
      ready_stable=$(kubectl -n "$NS" get rs -l "rollouts-pod-template-hash=${stable_hash}" \
        -o jsonpath='{.items[0].status.readyReplicas}')
      if [[ $canary_pods -eq 0 && ${ready_stable:-0} -eq $REPLICAS ]]; then
        return 0
      fi
    fi
    sleep 2
  done
  die "bad revision ${canary} was not rolled back within ${timeout}s"
}

pods_with_version() {
  kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=${RELEASE}" \
    -o jsonpath='{range .items[*]}{.spec.containers[0].image}{"\n"}{end}' | sort | uniq -c
}

# ---------------------------------------------------------------------------
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create namespace loadtest --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# 1. Initial release: the first revision of a Rollout skips canary steps.
deploy v1
h1=$(wait_new_revision "")
wait_promoted "$h1" 300
log "v1 healthy (hash ${h1})"

# 2. Start steady load for the rest of the test.
kubectl -n loadtest create configmap k6-script --from-file=load.js=e2e/k6/load.js \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n loadtest delete job k6-load --ignore-not-found >/dev/null
kubectl apply -f e2e/k6/job.yaml >/dev/null
kubectl -n loadtest wait --for=condition=Ready pod -l app.kubernetes.io/name=k6-load --timeout=180s >/dev/null \
  || die "k6 load generator did not start"
log "k6 load running; warming up metrics for 40s"
sleep 40
baseline_rps=$(prom_query "sum(rate(http_requests_total{namespace=\"${NS}\",route=\"/\"}[30s]))")
log "baseline throughput: ${baseline_rps} req/s"
[[ $baseline_rps != "NaN" ]] || die "Prometheus is not scraping demo-api"

# 3. Good release: must be fully promoted.
t0=$SECONDS
deploy v2
h2=$(wait_new_revision "$h1")
log "v2 canary started (hash ${h2})"
wait_promoted "$h2" "$PROMOTE_TIMEOUT"
promote_seconds=$((SECONDS - t0))
log "v2 fully promoted in ${promote_seconds}s"
pods_with_version >&2

# 4. Bad release: analysis must fail and the rollout must abort automatically.
t0=$SECONDS
deploy v3
h3=$(wait_new_revision "$h2")
log "v3 canary started (hash ${h3})"
wait_aborted "$h3" "$h2" "$ROLLBACK_TIMEOUT"
rollback_seconds=$((SECONDS - t0))
abort_seconds=$((ABORT_AT - t0))
log "v3 aborted after ${abort_seconds}s; traffic fully back on v2 after ${rollback_seconds}s"
pods_with_version >&2

[[ $(ro '{.status.stableRS}') == "$h2" ]] || die "stable ReplicaSet is not v2 after rollback"
[[ $(ro '{.status.phase}') == "Degraded" ]] || die "rollout phase is not Degraded after abort"
images=$(pods_with_version)
grep -q "${IMAGE_REPO}:v3" <<<"$images" && die "v3 pods still running after rollback"

# 5. Evidence: analysis measurements that failed the canary.
kubectl -n "$NS" get analysisrun -o json >"$RESULTS_DIR/analysisruns.json"
failed_run=$(jq -r --arg h "$h3" '[.items[] | select(.metadata.name | contains($h))
  | select(.status.phase == "Failed")][0].metadata.name // ""' "$RESULTS_DIR/analysisruns.json")
[[ -n $failed_run ]] || die "no failed AnalysisRun found for v3"
success_values=$(jq -r --arg n "$failed_run" '.items[] | select(.metadata.name == $n)
  | .status.metricResults[] | select(.name == "success-rate") | [.measurements[].value] | join(" ")' \
  "$RESULTS_DIR/analysisruns.json")
failed_value=$(jq -r --arg n "$failed_run" '.items[] | select(.metadata.name == $n)
  | .status.metricResults[] | select(.name == "success-rate")
  | [.measurements[] | select(.phase == "Failed")][-1].value' "$RESULTS_DIR/analysisruns.json")
canary_error_rate=$(jq -rn --arg v "$failed_value" '($v | gsub("[\\[\\] ]"; "") | tonumber) as $s | ((1 - $s) * 10000 | round) / 100')
v2_success=$(jq -r --arg h "$h2" '[.items[] | select(.metadata.name | contains($h))
  | .status.metricResults[] | select(.name == "success-rate") | .measurements[].value] | join(" ")' \
  "$RESULTS_DIR/analysisruns.json")
v2_p95=$(jq -r --arg h "$h2" '[.items[] | select(.metadata.name | contains($h))
  | .status.metricResults[] | select(.name == "p95-latency") | .measurements[].value] | join(" ")' \
  "$RESULTS_DIR/analysisruns.json")
analysis_message=$(jq -r --arg n "$failed_run" '.items[] | select(.metadata.name == $n) | .status.message // ""' \
  "$RESULTS_DIR/analysisruns.json")

# 6. After rollback, the service must be clean again (no 5xx in the last 30s).
log "waiting 45s, then checking post-rollback error rate"
sleep 45
post_errors=$(prom_query "sum(rate(http_requests_total{namespace=\"${NS}\",route=\"/\",code=~\"5..\"}[30s])) or vector(0)")
post_rps=$(prom_query "sum(rate(http_requests_total{namespace=\"${NS}\",route=\"/\"}[30s]))")
log "post-rollback: ${post_errors} 5xx req/s out of ${post_rps} req/s"
awk -v e="$post_errors" 'BEGIN { exit !(e + 0 == 0) }' || die "service still returns 5xx after rollback"

kubectl -n loadtest delete job k6-load --wait=false >/dev/null || true

jq -n \
  --arg kind_node "$(kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}')" \
  --argjson promote "$promote_seconds" \
  --argjson abort "$abort_seconds" \
  --argjson rollback "$rollback_seconds" \
  --arg failed_run "$failed_run" \
  --arg failed_value "$failed_value" \
  --arg canary_error_rate "$canary_error_rate" \
  --arg success_values "$success_values" \
  --arg v2_success "$v2_success" \
  --arg v2_p95 "$v2_p95" \
  --arg message "$analysis_message" \
  --arg baseline_rps "$baseline_rps" \
  --arg post_errors "$post_errors" \
  '{kubernetes: $kind_node, load_rps: $baseline_rps,
    v2_time_to_full_promotion_seconds: $promote,
    v2_success_rate_measurements: $v2_success, v2_p95_latency_measurements_seconds: $v2_p95,
    v3_time_to_abort_seconds: $abort, v3_time_to_full_rollback_seconds: $rollback,
    v3_failed_analysisrun: $failed_run, v3_success_rate_measurements: $success_values,
    v3_failing_success_rate: $failed_value, v3_canary_error_rate_percent: $canary_error_rate,
    v3_analysis_message: $message, post_rollback_5xx_per_second: $post_errors}' \
  >"$RESULTS_DIR/results.json"

cat "$RESULTS_DIR/results.json"

{
  echo "## Progressive delivery e2e results"
  echo
  echo "| Check | Result |"
  echo "|---|---|"
  echo "| Kubernetes (kind) | $(jq -r .kubernetes "$RESULTS_DIR/results.json") |"
  echo "| Load (k6, constant arrival) | ${baseline_rps} req/s |"
  echo "| v2 (good): time to full promotion | ${promote_seconds}s |"
  echo "| v2 canary success-rate measurements | ${v2_success} |"
  echo "| v2 canary p95 latency measurements (s) | ${v2_p95} |"
  echo "| v3 (bad): time to automatic abort | ${abort_seconds}s |"
  echo "| v3 (bad): time until all traffic back on v2 | ${rollback_seconds}s |"
  echo "| v3 canary success-rate measurements | ${success_values} |"
  echo "| v3 canary error rate that failed analysis | ${canary_error_rate}% |"
  echo "| Post-rollback 5xx rate | ${post_errors} req/s |"
  echo
  echo "Failed AnalysisRun \`${failed_run}\`: ${analysis_message}"
} >"$RESULTS_DIR/summary.md"

if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  cat "$RESULTS_DIR/summary.md" >>"$GITHUB_STEP_SUMMARY"
fi
log "PASS: good release promoted, bad release rolled back automatically"
