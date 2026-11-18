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

# Builds hack/kaniko-features with kaniko-build twice against the same cache
# scope (the second build extracts the cached layers) and checks the files
# that need privileges in the output image: owners, setuid/setgid bits and
# file capabilities.
# Usage: build.sh <cache repo>
set -euo pipefail

cache_repo="$1"
dir="$(cd "$(dirname "$0")" && pwd)"
out="${RUNNER_TEMP:-/tmp}/kaniko-features"
rm -rf "${out}" && mkdir -p "${out}"

for run in cold warm; do
  kaniko-build --context="${dir}" --dockerfile=Dockerfile --no-push \
    --build-arg=NONCE="${run}-$$" --cache=true --cache-repo="${cache_repo}" \
    --tar-path="${out}/${run}.tar" > "${out}/${run}.log" 2>&1 \
    || { tail -20 "${out}/${run}.log"; echo "::error::${run} build failed"; exit 1; }
  hits=$(grep -c "Using caching version of cmd" "${out}/${run}.log" || true)
  echo "${run}: ok, cache hits=${hits}"
done
[ "${hits}" -ge 5 ] || { echo "::error::the warm build did not use the cache"; exit 1; }

python3 - "${out}/warm.tar" <<'PY'
import io, json, sys, tarfile
image = tarfile.open(sys.argv[1])
files = {}
for layer in json.load(image.extractfile("manifest.json"))[0]["Layers"]:
    for m in tarfile.open(fileobj=io.BytesIO(image.extractfile(layer).read())):
        files[m.name.lstrip("./")] = m
want = {
    "usr/local/bin/suid": lambda m: (m.mode, m.uid, m.gid) == (0o6755, 1500, 1500),
    "usr/local/bin/fcap": lambda m: "SCHILY.xattr.security.capability" in m.pax_headers,
    "private": lambda m: (m.mode, m.uid) == (0o700, 1500),
    "private/root.txt": lambda m: m.uid == 0,
    "home/app/payload.txt": lambda m: m.uid == 1500,
    "home/app/as-app.txt": lambda m: m.uid == 1500,
}
bad = [p for p, ok in want.items() if p not in files or not ok(files[p])]
if bad:
    sys.exit("::error::unexpected image entries: " + ", ".join(bad))
print("image entries: ok")
PY
