#!/usr/bin/env bash

set -Eeuo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
result_path=""
image_ref="waybill-guardian:delivery-acceptance"
govulncheck_version="v1.8.0"
syft_version="1.54.1"
trivy_version="0.75.0"

while (($# > 0)); do
  case "$1" in
    --result)
      result_path="${2:?--result requires a path}"
      shift 2
      ;;
    --image)
      image_ref="${2:?--image requires a reference}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

test -n "$result_path"
for command in curl docker go jq npm tar; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required" >&2
    exit 1
  }
done

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
tool_dir="$tmp_dir/bin"
trivy_cache="${TRIVY_CACHE_DIR:-$HOME/.cache/waybill-trivy}"
mkdir -p "$tool_dir"
mkdir -p "$trivy_cache"
case "$(uname -s):$(uname -m)" in
  Darwin:arm64)
    syft_asset="syft_${syft_version}_darwin_arm64.tar.gz"
    trivy_asset="trivy_${trivy_version}_macOS-ARM64.tar.gz"
    ;;
  Darwin:x86_64)
    syft_asset="syft_${syft_version}_darwin_amd64.tar.gz"
    trivy_asset="trivy_${trivy_version}_macOS-64bit.tar.gz"
    ;;
  Linux:aarch64 | Linux:arm64)
    syft_asset="syft_${syft_version}_linux_arm64.tar.gz"
    trivy_asset="trivy_${trivy_version}_Linux-ARM64.tar.gz"
    ;;
  Linux:x86_64)
    syft_asset="syft_${syft_version}_linux_amd64.tar.gz"
    trivy_asset="trivy_${trivy_version}_Linux-64bit.tar.gz"
    ;;
  *)
    echo "unsupported scanner platform: $(uname -s)/$(uname -m)" >&2
    exit 1
    ;;
esac

download_and_verify() {
  local repository="$1"
  local version="$2"
  local asset="$3"
  local checksums="$4"
  local release_url="https://github.com/${repository}/releases/download/v${version}"
  curl --fail --silent --show-error --location \
    --output "$tmp_dir/$asset" "$release_url/$asset"
  curl --fail --silent --show-error --location \
    --output "$tmp_dir/$checksums" "$release_url/$checksums"
  if command -v shasum >/dev/null 2>&1; then
    (
      cd "$tmp_dir"
      grep -F "  $asset" "$checksums" | shasum -a 256 --check -
    )
  else
    (
      cd "$tmp_dir"
      grep -F "  $asset" "$checksums" | sha256sum --check -
    )
  fi
  tar -C "$tool_dir" -xzf "$tmp_dir/$asset"
}

download_and_verify \
  "anchore/syft" "$syft_version" "$syft_asset" \
  "syft_${syft_version}_checksums.txt"
download_and_verify \
  "aquasecurity/trivy" "$trivy_version" "$trivy_asset" \
  "trivy_${trivy_version}_checksums.txt"

cd "$root_dir"
env -u GOROOT GOBIN="$tool_dir" \
  go install "golang.org/x/vuln/cmd/govulncheck@${govulncheck_version}"
"$tool_dir/govulncheck" ./...

(
  cd web
  npm audit --omit=dev --audit-level=high
)
./scripts/licenses.sh

docker build --check .
docker build --tag "$image_ref" .
container="$(docker create "$image_ref")"
trap 'docker rm --volumes "$container" >/dev/null 2>&1 || true; rm -rf "$tmp_dir"' EXIT
docker export "$container" | tar -tf - >"$tmp_dir/image-files.txt"
test "$(grep -Ec '^app/third_party_licenses/go/.+' "$tmp_dir/image-files.txt")" -gt 0
test "$(grep -Ec '^app/third_party_licenses/web/.+' "$tmp_dir/image-files.txt")" -gt 0
if grep -Eq '\.(go|tsx?|env)$|(^|/)go\.(mod|sum)$|(^|/)package(-lock)?\.json$' \
  "$tmp_dir/image-files.txt"; then
  echo "runtime image contains source or environment files" >&2
  exit 1
fi

"$tool_dir/syft" "docker:${image_ref}" \
  --output cyclonedx-json >"$tmp_dir/image.cdx.json"
jq -e '
  .bomFormat == "CycloneDX" and
  (.specVersion | type == "string") and
  (.components | type == "array" and length > 0)
' "$tmp_dir/image.cdx.json" >/dev/null

"$tool_dir/trivy" image \
  --cache-dir "$trivy_cache" \
  --exit-code 1 \
  --ignore-unfixed \
  --severity HIGH,CRITICAL \
  "$image_ref"

if command -v shasum >/dev/null 2>&1; then
  sbom_sha="$(shasum -a 256 "$tmp_dir/image.cdx.json" | awk '{print $1}')"
else
  sbom_sha="$(sha256sum "$tmp_dir/image.cdx.json" | awk '{print $1}')"
fi
sbom_bytes="$(wc -c <"$tmp_dir/image.cdx.json" | tr -d ' ')"
mkdir -p "$(dirname "$result_path")"

printf '%s\n' \
  '{' \
  '  "schema_version": "delivery.acceptance.command-result.v1",' \
  '  "probe_id": "supply-chain",' \
  '  "checks": [' \
  '    {"id":"go_vulnerabilities","passed":true,"detail":"govulncheck reported no reachable vulnerability"},' \
  '    {"id":"web_vulnerabilities","passed":true,"detail":"npm audit reported no high or critical production vulnerability"},' \
  '    {"id":"licenses","passed":true,"detail":"committed Go and web license manifests are current"},' \
  '    {"id":"container_contents","passed":true,"detail":"runtime image contains licenses and excludes source and environment files"},' \
  '    {"id":"container_vulnerabilities","passed":true,"detail":"Trivy reported no fixable high or critical image vulnerability"},' \
  '    {"id":"cyclonedx_sbom","passed":true,"detail":"Syft generated a nonempty CycloneDX image SBOM"}' \
  '  ],' \
  '  "measurements": [],' \
  '  "artifacts": [' \
  "    {\"name\":\"image.cdx.json\",\"sha256\":\"${sbom_sha}\",\"bytes\":${sbom_bytes}}" \
  '  ]' \
  '}' >"$result_path"
