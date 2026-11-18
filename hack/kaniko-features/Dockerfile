# Copyright 2026 Google, Inc. All rights reserved.
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

# Exercises the Dockerfile features that need privileges inside the build, so
# the runner's capability set can be checked against them.
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
# apt drops to the _apt user for downloads.
RUN apt-get update && apt-get install -y --no-install-recommends libcap2-bin iputils-ping && rm -rf /var/lib/apt/lists/*
# Root writes into a directory only another user may access.
RUN useradd -m -u 1500 app \
 && install -d -o app -g app -m 700 /private \
 && echo root > /private/root.txt
# setuid/setgid bits on a file owned by another user and group.
RUN cp /bin/true /usr/local/bin/suid \
 && chown app:app /usr/local/bin/suid \
 && chmod 6755 /usr/local/bin/suid \
 && test -u /usr/local/bin/suid && test -g /usr/local/bin/suid
# File capabilities.
RUN cp /bin/true /usr/local/bin/fcap \
 && setcap cap_net_bind_service+ep /usr/local/bin/fcap \
 && getcap /usr/local/bin/fcap | grep -q cap_net_bind_service
COPY --chown=app:app payload.txt /home/app/payload.txt
USER app
# kaniko kills what a RUN step leaves behind, here a process of another user.
RUN [ "$(id -u):$(id -g)" = 1500:1500 ] && cat /home/app/payload.txt > /home/app/as-app.txt \
 && (sleep 300 >/dev/null 2>&1 &)
USER root
# Changes on every build, so the layers above are extracted from the cache
# before this step checks them.
ARG NONCE
RUN echo "${NONCE}" > /nonce \
 && [ "$(stat -c %a:%U:%G /usr/local/bin/suid)" = 6755:app:app ] \
 && getcap /usr/local/bin/fcap | grep -q cap_net_bind_service \
 && [ "$(stat -c %a:%U /private)" = 700:app ] && [ "$(cat /private/root.txt)" = root ] \
 && [ "$(stat -c %U /home/app/as-app.txt)" = app ] \
 && setpriv --reuid=1500 --regid=1500 --clear-groups /usr/local/bin/fcap \
 && capsh --drop=cap_net_raw -- -c true \
 && ping -c 1 -W 2 127.0.0.1 >/dev/null
