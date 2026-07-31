#!/usr/bin/env bash

# Copyright Authors of Cilium
# SPDX-License-Identifier: Apache-2.0

set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage:
  package-chart.sh CHART_VERSION [IMAGE_TAG] [REGISTRY_NAMESPACE] [OUTPUT_DIR]

Example:
  package-chart.sh 1.19.6-oci.2 v1.19.6-oci.2 ghcr.io/xmltiger dist
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ "$#" -lt 1 || "$#" -gt 4 ]]; then
  usage >&2
  exit 2
fi

chart_version="${1#v}"
image_tag="${2:-v${chart_version}}"
registry_namespace="${3:-ghcr.io/xmltiger}"
output_dir="${4:-dist}"

if [[ ! "${chart_version}" =~ ^1\.19\.[0-9]+-oci\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "Invalid chart version: ${chart_version}" >&2
  exit 1
fi

if [[ "${image_tag}" != "v${chart_version}" ]]; then
  echo "Image tag must be v${chart_version}, got ${image_tag}" >&2
  exit 1
fi

for command_name in git helm perl sed; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Required command not found: ${command_name}" >&2
    exit 1
  fi
done

repo_root="$(git rev-parse --show-toplevel)"
base_version="$(tr -d '[:space:]' < "${repo_root}/VERSION")"
if [[ "${chart_version}" != "${base_version}-oci."* ]]; then
  echo "Chart version ${chart_version} must be based on VERSION=${base_version}" >&2
  exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

chart_dir="${work_dir}/cilium"
cp -R "${repo_root}/install/kubernetes/cilium" "${chart_dir}"

CHART_VERSION="${chart_version}" perl -0pi -e \
  's/^version: .+$/version: $ENV{CHART_VERSION}/m; s/^appVersion: .+$/appVersion: $ENV{CHART_VERSION}/m' \
  "${chart_dir}/Chart.yaml"

REGISTRY_NAMESPACE="${registry_namespace}" IMAGE_TAG="${image_tag}" BASE_VERSION="${base_version}" \
  perl -0pi -e '
    s#^(\s*)repository: "quay\.io/cilium/cilium"\n(\s*)tag: "v\Q$ENV{BASE_VERSION}\E"#$1repository: "$ENV{REGISTRY_NAMESPACE}/cilium"\n$2tag: "$ENV{IMAGE_TAG}"#gm;
    s#^(\s*)repository: "quay\.io/cilium/operator"\n(\s*)tag: "v\Q$ENV{BASE_VERSION}\E"#$1repository: "$ENV{REGISTRY_NAMESPACE}/operator"\n$2tag: "$ENV{IMAGE_TAG}"#gm;
    s#^(\s*)repository: "quay\.io/cilium/hubble-relay"\n(\s*)tag: "v\Q$ENV{BASE_VERSION}\E"#$1repository: "$ENV{REGISTRY_NAMESPACE}/hubble-relay"\n$2tag: "$ENV{IMAGE_TAG}"#gm;
  ' "${chart_dir}/values.yaml"

if grep -qE 'repository: "quay\.io/cilium/(cilium|operator|hubble-relay)"' "${chart_dir}/values.yaml"; then
  echo "Failed to replace all release image repositories in values.yaml" >&2
  exit 1
fi

mkdir -p "${output_dir}"
values_file="${output_dir}/cilium-${chart_version}-oci-values.yaml"
sed \
  -e "s#__REGISTRY_NAMESPACE__#${registry_namespace}#g" \
  -e "s#__IMAGE_TAG__#${image_tag}#g" \
  "${repo_root}/contrib/oci-release/values-oci.example.yaml" > "${values_file}"

helm lint "${chart_dir}" --values "${values_file}"
helm template cilium "${chart_dir}" \
  --namespace kube-system \
  --values "${values_file}" \
  > /dev/null

helm package "${chart_dir}" \
  --destination "${output_dir}" \
  --version "${chart_version}" \
  --app-version "${chart_version}"

echo "Created ${output_dir}/cilium-${chart_version}.tgz"
echo "Created ${values_file}"
