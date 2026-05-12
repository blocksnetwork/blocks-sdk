# Python agent network-isolation test harness. Mirrors
# examples/other/agent-docker/Dockerfile. Differences:
#  - python:3.12-slim base image
#  - blocks-network Python SDK installed via `pip install -e /sdk`
#  - agent project bind-mounted at /agent; entrypoint cd's into it
#    and runs `blocks-run` (the SDK's console_script entry).
#
# Build context is the repo root (set in docker-compose.yml).

FROM python:3.12-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates socat curl gnupg \
 && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/*

# Copy the SDK source. We copy pyproject.toml first so Docker's layer
# cache survives unrelated source edits during iteration.
COPY blocks-sdk/sdks/python/pyproject.toml /sdk/pyproject.toml
COPY blocks-sdk/sdks/python/blocks_network /sdk/blocks_network
COPY blocks-sdk/sdks/python/scripts /sdk/scripts
RUN pip install --no-cache-dir -e /sdk

COPY examples/other/agent-docker-python/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY examples/other/agent-docker-python/log-prefix.mjs /usr/local/bin/log-prefix.mjs
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/log-prefix.mjs

WORKDIR /agent
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
