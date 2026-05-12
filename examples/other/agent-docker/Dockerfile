# Network-isolation test harness for Blocks agents.
#
# Runs an agent on a bridge network so `docker network disconnect` can
# simulate PubNub connectivity loss for the agent only — the host
# browser and backend stay online. This is the canonical way to
# exercise BLOCKS-129 presence scenarios end-to-end (see the TEST_PLAN
# under dev_docs/initiative/05-04_presence_dot_fixes/).
#
# The locally-built Node SDK is copied into the image and npm-installed
# inside the container (producing platform-native binaries). The agent
# project is bind-mounted at runtime for fast iteration.
#
# The local CDM returns `api.baseUrl: http://localhost:{PORT}`, which
# from inside the container is its own loopback. A socat forwarder
# relays container-local 127.0.0.1:{PORT} → host.docker.internal:{PORT}
# so the SDK uses the CDM-provided URL verbatim. PORT defaults to 3011
# (matching this worktree's backend .env) and is overridable.
#
# Build context is the repo root (set in docker-compose.yml).

FROM node:24-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates socat \
 && rm -rf /var/lib/apt/lists/*

# Copy the locally-built Node SDK. Requires `cd blocks-sdk && npm run
# build` on the host first so `dist/` is populated.
COPY blocks-sdk/sdks/node/package.json /sdk/package.json
COPY blocks-sdk/sdks/node/dist /sdk/dist
RUN cd /sdk && npm install --omit=dev

# Make the SDK resolvable as `@blocks-network/sdk` from anywhere.
RUN mkdir -p /node_modules/@blocks-network \
 && ln -s /sdk /node_modules/@blocks-network/sdk

COPY examples/other/agent-docker/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY examples/other/agent-docker/log-prefix.mjs /usr/local/bin/log-prefix.mjs
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/log-prefix.mjs

WORKDIR /agent
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
