#!/usr/bin/env bash

# Copyright Authors of Cilium
# SPDX-License-Identifier: Apache-2.0

set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage:
  GHCR_USERNAME=xmltiger GHCR_TOKEN=... publish-local.sh [CHART_VERSION]

Environment variables:
  GHCR_USERNAME       GHCR user name. Required.
  GHCR_TOKEN          Classic PAT with write:packages and read:packages. Required.
  REGISTRY_NAMESPACE Defaults to ghcr.io/xmltiger.
  OUTPUT_DIR          Defaults to dist.
  BUILDER_NAME        Defaults to cilium-oci-release.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ "$#" -gt 1 ]]; then
  usage >&2
  exit 2
fi

chart_version="${1:-1.19.6-oci.1}"
chart_version="${chart_version#v}"
image_tag="v${chart_version}"
registry_namespace="${REGISTRY_NAMESPACE:-ghcr.io/xmltiger}"
registry_host="${registry_namespace%%/*}"
output_dir="${OUTPUT_DIR:-dist}"
builder_name="${BUILDER_NAME:-cilium-oci-release}"

: "${GHCR_USERNAME:?GHCR_USERNAME is required}"
: "${GHCR_TOKEN:?GHCR_TOKEN is required}"

for command_name in docker helm git; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Required command not found: ${command_name}" >&2
    exit 1
  fi
done

repo_root="$(git rev-parse --show-toplevel)"
cd "${repo_root}"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "The working tree must be clean before a release." >&2
  exit 1
fi

printf '%s' "${GHCR_TOKEN}" |
  docker login "${registry_host}" \
    --username "${GHCR_USERNAME}" \
    --password-stdin
printf '%s' "${GHCR_TOKEN}" |
  helm registry login "${registry_host}" \
    --username "${GHCR_USERNAME}" \
    --password-stdin

if ! docker buildx inspect "${builder_name}" >/dev/null 2>&1; then
  docker buildx create \
    --name "${builder_name}" \
    --driver docker-container \
    --platform linux/amd64,linux/arm64 \
    --use
else
  docker buildx use "${builder_name}"
fi
docker buildx inspect --bootstrap

common_args=(
  --builder "${builder_name}"
  --platform linux/amd64,linux/arm64
  --target release
  --push
  --pull
  --provenance mode=max
  --sbom true
)

docker buildx build \
  "${common_args[@]}" \
  --file images/cilium/Dockerfile \
  --tag "${registry_namespace}/cilium:${image_tag}" \
  .

docker buildx build \
  "${common_args[@]}" \
  --file images/operator/Dockerfile \
  --build-arg OPERATOR_VARIANT=operator-oci \
  --tag "${registry_namespace}/operator-oci:${image_tag}" \
  .

docker buildx build \
  "${common_args[@]}" \
  --file images/hubble-relay/Dockerfile \
  --tag "${registry_namespace}/hubble-relay:${image_tag}" \
  .

bash contrib/oci-release/package-chart.sh \
  "${chart_version}" \
  "${image_tag}" \
  "${registry_namespace}" \
  "${output_dir}"

helm push \
  "${output_dir}/cilium-${chart_version}.tgz" \
  "oci://${registry_namespace}/charts"

for image_name in cilium operator-oci hubble-relay; do
  docker buildx imagetools inspect \
    "${registry_namespace}/${image_name}:${image_tag}"
done

echo "Published chart: oci://${registry_namespace}/charts/cilium:${chart_version}"
