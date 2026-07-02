# Embed Multi-Agent Pages

This guide explains how to connect a single hosted page to more than one Blocks
agent using the embedded-auth widget.

For product context see [HOST_PAGES_PLAN.md](../dev_docs/initiative/host_static_pages/HOST_PAGES_PLAN.md).
For the widget API reference see [impl_03_widget.md](../dev_docs/initiative/host_static_pages/impl_03_widget.md).

---

## 1. Why multi-agent?

When a user signs in via the Blocks popup, the widget performs **one**
authentication round-trip and issues **one** JWT that covers all the agents
listed on the page. Subsequent calls reuse the same token, and refresh happens
in a single background loop — no matter how many agents the page calls.

From your code's perspective you get one `TaskClient` per agent, all
authenticated under the same session. The result:

- **One popup** — the user authenticates once regardless of how many agents
  the page uses.
- **One JWT** — a single token scoped to all listed agents.
- **One refresh loop** — token renewal runs in the background and covers every
  client transparently.
- **Multiple `TaskClient` instances** — each agent gets its own client object
  for independent task dispatch.

---

## 2. Scaffolding a multi-agent page

The CLI's webapp scaffolder supports multi-agent pages natively — list every
agent on the `--agent` flag (repeated or comma-separated):

```bash
blocks init my_pipeline --mode webapp \
  --agent transcribe --agent summarize --agent translate
# or, equivalently:
blocks init my_pipeline --mode webapp \
  --agent transcribe,summarize,translate
```

`blocks init` fetches each named agent's card from the registry at scaffold
time and emits per-agent input/output/stream wiring into a single
`web/app.js`, plus a labeled HTML section per agent. Re-run `blocks init` in
a fresh directory to refresh against the latest cards.

The generated `blocks.config.json` lists every agent in the `agents` array.
Use bare agent names (no slashes — matching the `^[a-zA-Z0-9_]+$` registry
pattern):

```json
{
  "templateVersion": "1.0.0",
  "agents": ["transcribe", "summarize", "translate"]
}
```

The `agents` array is what `blocks dev` and `blocks deploy` read to determine
which agents the page intends to call. There is no per-page origin
declaration on the agent card — access is gated entirely by the user's
signed-in identity and (for private agents) BLOCKS-162 invitations / grants.

Maximum 25 agents per page (schema constraint).

---

## 3. Widget call: `signInAndGetClients`

Use `BlocksAuth.signInAndGetClients` (plural) to authenticate and receive a
map of clients keyed by agent name:

```js
const clients = await BlocksAuth.signInAndGetClients({
  agents: ['transcribe', 'summarize', 'translate'],
});

// clients.transcribe  — TaskClient for transcribe
// clients.summarize   — TaskClient for summarize
// clients.translate   — TaskClient for translate
```

Each value is a full `TaskClient` with `sendMessage` and related methods.

If you only need one agent, use `BlocksAuth.signInAndGetClient` (singular):

```js
const client = await BlocksAuth.signInAndGetClient({ agent: 'translate' });
```

---

## 4. Worked example: transcribe → summarize → translate pipeline

The following example takes an audio transcript, summarizes it, then translates
the summary — chaining three agents in sequence.

```html
<!-- index.html -->
<script src="/__blocks_embed_dev.js"></script>
<script src="https://app.blocks.ai/embed/auth.0.1.0.min.js"></script>
<script src="app.js" defer></script>
```

```js
// app.js
(async () => {
  const signInBtn = document.getElementById('sign-in-btn');
  const outputArea = document.getElementById('output');

  let clients = null;

  signInBtn.addEventListener('click', async () => {
    clients = await BlocksAuth.signInAndGetClients({
      agents: ['transcribe', 'summarize', 'translate'],
    });
    document.getElementById('controls').hidden = false;
    signInBtn.hidden = true;
  });

  // Helper: run a task and return the text of the first artifact.
  // `partId` matches the agent's declared `io.inputs[0].id`; the
  // scaffolder wires this per agent — replace the placeholder
  // below with whatever each card declares.
  async function runTask(client, agentName, text, partId) {
    const session = await client.sendMessage({
      agentName,
      requestParts: [{ partId, text }],
    });
    const terminal = await session.waitForTerminal();
    if (terminal.state !== 'completed') {
      throw new Error('Task failed: ' + (terminal.error ?? terminal.reason ?? 'unknown'));
    }
    const artifacts = session.listArtifacts();
    if (artifacts.length === 0) return '';
    const downloaded = await session.downloadArtifact(artifacts[0]);
    return new TextDecoder().decode(downloaded.data);
  }

  document.getElementById('run-pipeline').addEventListener('click', async () => {
    const audioUrl = document.getElementById('audio-url').value.trim();
    if (!audioUrl || !clients) return;

    // Replace the third `partId` argument with each agent's declared
    // `io.inputs[0].id` from its card. The scaffolder fills these in
    // for you when you run `blocks init <name> --mode webapp --agent ...`.
    outputArea.textContent = 'Transcribing...';
    const transcript = await runTask(clients.transcribe, 'transcribe', audioUrl, 'audio');

    outputArea.textContent = 'Summarizing...';
    const summary = await runTask(clients.summarize, 'summarize', transcript, 'text');

    outputArea.textContent = 'Translating...';
    const translated = await runTask(clients.translate, 'translate', summary, 'text');

    outputArea.textContent = translated;
  });
})();
```

`blocks.config.json` for the above:

```json
{
  "templateVersion": "1.0.0",
  "agents": ["transcribe", "summarize", "translate"]
}
```

---

## 5. Caveat: cross-org private agents (`MULTI_ORG_PRIVATE_AGENTS_NOT_SUPPORTED`)

The embedded-auth popup issues a single JWT scoped to the agents listed on the
page. All agents in the list **must belong to the same organization** if any of
them are private.

If you mix private agents from different organizations, the popup flow returns
the error code `MULTI_ORG_PRIVATE_AGENTS_NOT_SUPPORTED`. In this case the page
must either:

- Remove the private agent from the list, or
- Ask the other organization to make their agent public, or
- Split the page into separate pages — one per organization.

Public agents (listed in the Blocks registry without access restrictions) can
be mixed freely across organizations.

---

## 6. Caveat: private agents require an invitation

There is no per-page origin declaration on the agent card — any partner page
can list any public agent, and the popup will mint a JWT scoped to the agents
the signed-in user can reach. The user's Blocks identity is the trust anchor
(by completing sign-in in the popup, the user is consenting that "this site
can call agents on my behalf").

For **public** agents, no setup is required — every signed-in user can call
the agent through any embed page.

For **private** agents, the calling user must be a member of the owning
organization or hold an invitation grant. If the user lacks reach, the agent
is silently absent from the JWT scope (it is not an enumeration oracle); the
page's `runTask` call against that agent will fail with an authorization
error at task-create time.

**For agents owned by another organization:** the partner-page author has
nothing to configure on the agent card. If a user is hitting authorization
errors against a private agent, ask the agent owner to issue an invitation
grant via the Blocks dashboard (BLOCKS-162). Public agents are usable by any
signed-in Blocks user without coordination.
