#!/usr/bin/env bash
# Opens a pull request that sets the image tag of one environment.
#   scripts/promote.sh <environment> <image-tag> [reason]
# Requires git push rights and GH_TOKEN for the gh CLI. Idempotent: does nothing
# when the environment already runs the tag or a PR for it is already open.
set -euo pipefail

env_name=${1:?environment (dev|staging|prod) required}
tag=${2:?image tag required}
reason=${3:-"Promote ${tag} to ${env_name}"}

values="environments/${env_name}/values.yaml"
[[ -f $values ]] || { echo "unknown environment: ${env_name}" >&2; exit 1; }
[[ $tag =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || { echo "invalid tag: ${tag}" >&2; exit 1; }

current_tag() { sed -nE 's/^  tag: *"?([^" #]+)"?.*$/\1/p' "$1" | head -n1; }

current=$(current_tag "$values")
if [[ $current == "$tag" ]]; then
  echo "${env_name} already runs ${tag}; nothing to do"
  exit 0
fi

branch="promote/${env_name}-${tag}"
if git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
  echo "branch ${branch} already exists; promotion PR is pending"
  exit 0
fi

git switch -c "$branch"
sed -i -E "s/^(  tag: *)\"?[^\" #]+\"?/\1${tag}/" "$values"
[[ $(current_tag "$values") == "$tag" ]] || { echo "failed to update ${values}" >&2; exit 1; }

git add "$values"
git commit -m "deploy(${env_name}): promote demo-api to ${tag}" -m "${reason}"
git push origin "$branch"

gh pr create \
  --base main \
  --head "$branch" \
  --title "deploy(${env_name}): promote demo-api to ${tag}" \
  --label promotion \
  --body "$(cat <<EOF
Promotes **demo-api** in **${env_name}** from \`${current}\` to \`${tag}\`.

${reason}

Merging this PR is the deployment: Argo CD syncs \`environments/${env_name}\`
and Argo Rollouts runs the canary with Prometheus analysis. A failed analysis
aborts the rollout automatically and keeps \`${current}\` serving.
EOF
)"
