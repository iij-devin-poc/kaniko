#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Builds hack/gha-cache-e2e with kaniko-build and the GitHub Actions layer cache,
# then checks the cache lookups. kaniko stops looking up layers after the first
# miss, so a build has at most one miss; every layer built after it is saved.
# Usage: build.sh <name> <cache scope> <expected hits> <expected misses> <expected saves>
set -euo pipefail

name="$1" scope="$2" want_hits="$3" want_misses="$4" want_saved="$5"
dir="$(cd "$(dirname "$0")" && pwd)"
out="${RUNNER_TEMP:-/tmp}/gha-cache-e2e/${name}"
rm -rf "${out}" && mkdir -p "${out}"

start=$(date +%s)
status=0
kaniko-build --context="${dir}" --dockerfile=Dockerfile --no-push --reproducible \
  --cache=true --cache-repo="gha:${scope}" \
  --digest-file="${out}/digest" --tar-path="${out}/image.tar" 2>&1 | tee "${out}/log" || status=$?
seconds=$(( $(date +%s) - start ))

hits=$(grep -c "Using caching version of cmd" "${out}/log" || true)
misses=$(grep -c "No cached layer found for cmd" "${out}/log" || true)
saved=$(grep -c "Pushing layer kaniko-.* to cache now" "${out}/log" || true)
digest=$(cat "${out}/digest" 2>/dev/null || echo none)

# The image must contain the outputs of every RUN step, cached or not.
mkdir -p "${out}/image" "${out}/rootfs"
tar -xf "${out}/image.tar" -C "${out}/image"
for layer in $(jq -r '.[0].Layers[]' "${out}/image/manifest.json"); do
  tar -xzf "${out}/image/${layer}" -C "${out}/rootfs" 2>/dev/null || tar -xf "${out}/image/${layer}" -C "${out}/rootfs"
done
contents=ok
for f in slow.txt usr/bin/curl payload.txt payload.sha256; do
  [ -e "${out}/rootfs/${f}" ] || contents="missing /${f}"
done
if ! (cd "${out}/rootfs" && sed 's#  /#  #' payload.sha256 | sha256sum -c --quiet); then
  contents="stale /payload.sha256"
fi

echo "| ${name} | gha:${scope} | ${seconds}s | ${hits} | ${misses} | ${saved} | \`${digest:7:12}\` | ${contents} |" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
echo "seconds=${seconds}" >> "${GITHUB_OUTPUT:-/dev/null}"
echo "digest=${digest}" >> "${GITHUB_OUTPUT:-/dev/null}"
echo "${name}: ${seconds}s, cache hits=${hits} misses=${misses} saved=${saved}, digest=${digest}, contents=${contents}"

[ "${status}" = 0 ] || { echo "::error::kaniko-build failed (${status})"; exit 1; }
[ "${hits}" = "${want_hits}" ] || { echo "::error::expected ${want_hits} cache hits, got ${hits}"; exit 1; }
[ "${misses}" = "${want_misses}" ] || { echo "::error::expected ${want_misses} cache misses, got ${misses}"; exit 1; }
[ "${saved}" = "${want_saved}" ] || { echo "::error::expected ${want_saved} layers saved, got ${saved}"; exit 1; }
[ "${contents}" = ok ] || { echo "::error::image check failed: ${contents}"; exit 1; }
