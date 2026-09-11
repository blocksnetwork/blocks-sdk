---
name: blocks-network
description: Modify, validate, run, register, publish, consume, and troubleshoot agents on Blocks Network. Use when an agent project already exists, when deploying existing code, or when working with agent-card.json, the Blocks CLI, TaskClient, streams, private access, or publishing. For a brand-new agent, use blocks-getstarted. Do not use for generic AI-agent work or for a Blocks Enterprise deployment, which serves its own skill at <deployment>/skill.md.
metadata:
  author: blocks-network
  version: "0.4.0"
---

# Blocks Network

Work on Blocks agents on Blocks Network, the public multi-tenant Blocks
deployment. The CLI and SDKs target it by default; `blocks login --network`
selects it explicitly.

A Blocks Enterprise deployment is a separate single-tenant instance with its own
authentication, discovery, and visibility. It serves its own skill at
`<deployment>/skill.md`. Do not configure against one unless the user names it.

## Route before acting

Load only the material needed for the request:

- **Create a brand-new agent:** use [blocks-getstarted] and follow its linear
  workflow. Do not duplicate that workflow here.
- **Edit or create `agent-card.json`:** read the [Agent Card Schema] first. It
  is authoritative for field names, required fields, types, and conditional
  validation. Then read [Agent Card Reference].
- **Change inputs or outputs:** also read [IO Schema Reference]. Keep the card's
  IO declarations aligned with what the handler accepts and returns.
- **TypeScript handlers, TaskClient, A2A, streams, or trigger scripts:** read
  [Node Reference]. TypeScript is the default for new code.
- **Python:** read [Python Reference] only when the user requests Python or the
  existing project is Python. Preserve the language of existing projects.
- **End-to-end development or deployment questions:** read [Agent Development
  Guide].

When a schema and prose disagree, the schema wins. Examples illustrate patterns;
they are not templates to copy without adapting names, descriptions, IO, task
kinds, timeouts, and stream declarations.

## Working method

1. Determine whether the user wants to modify an agent, deploy existing code,
   call an agent, manage access, or troubleshoot. Ask only when the distinction
   changes the work materially.
2. Locate candidate projects by `agent-card.json`, not by directory name. If
   more than one candidate fits, ask which one to use.
3. For an existing project, read its card and the file named by
   `runtime.handler` before editing. Treat `identity.agentName` as the agent's
   name even when the directory name differs.
4. Make the requested local changes. If handler input, output, task kind, or
   streaming behavior changes, update the matching card fields in the same
   change.
5. Run the smallest relevant local checks, including `blocks check` for every
   card change. Report failures with their actionable cause; do not claim the
   agent is deployed or running based only on local validation.
6. Hand off any live command the user must run, with the exact project path and
   agent name substituted.

Do not silently invent registry metadata, visibility, or pricing. The agent
name, description, owning organization when ambiguous, private versus public
listing, and free versus paid billing are user decisions.

## Network invariants

### Target Blocks Network

Authenticate against Blocks Network explicitly:

```bash
blocks login --network --write-env
```

In a non-interactive environment, always pass an explicit `--write-env` or
`--no-write-env`; without a flag, a non-TTY login succeeds but silently leaves
`.env` untouched. Use `--dir <project>` when logging in from a parent directory
so the API key is written to the intended project's `.env`.

A `--network` login writes only `BLOCKS_API_KEY` and removes any
`BLOCKS_BACKEND_URL` / `BLOCKS_CDM_URL` left by an earlier Enterprise login;
the SDK's built-in defaults reach Blocks Network. Plain trigger and consumer
processes launched outside `blocks run` need the key in their `.env`:

```dotenv
BLOCKS_API_KEY=<secret>
```

Never hardcode, print, commit, or send `BLOCKS_API_KEY` to browser code. Browser
apps must use a supported user-auth flow from the Node reference rather than a
provider API key.

### Registration and visibility

- `blocks register` is the recommended first registration. It registers the
  agent as free and private.
- A private agent is reachable only by its owner and by users or organizations
  holding an active grant. Owning-organization membership confers nothing;
  the owner restores org-wide access by granting their own organization.
- `blocks publish` makes the agent public and/or paid on Blocks Network. In a
  non-interactive shell it does not prompt: pass `--billing-mode {free|paid}`
  and `--listing {public|private}`; when paid, add `--accept-terms` and
  `--price` (or `--price-per-task` / `--price-per-minute` for dual-kind
  agents). Pass `--org-name` on the organization's first publish; when omitted
  the organization silently keeps its current name.
- Consumer `TaskClient` instances must pass the `billingMode` the agent is
  registered with; a mismatch is rejected with `BillingModeMismatchError`.
  When it is unknown, look it up with `getAgent(name, { baseUrl, apiKey })`.
  `getAgent` has no default backend URL and throws `baseUrl is required`
  without one, so take `baseUrl` from the CDM:
  `(await fetchCdmConfig()).api.baseUrl`. Pass `apiKey` too when the agent is
  private, or the lookup returns `null`. The Node and Python references show
  the full lookup.
- After an agent has been published, use `blocks publish` for card updates.
  Running `blocks register` again resets its listing to private and free.

Registering, publishing, inviting, revoking access, and starting a long-running
agent all change external or live state. Perform them only when the user
explicitly requests that action and the host permits it. Otherwise give the
user the exact command to run.

### Agent and task contracts

- `identity.agentName` matches `^[a-zA-Z0-9_]+$`; hyphens are not allowed.
- Use the Blocks SDK and `TaskClient` for task submission. Do not construct raw
  PubNub control or task-channel messages in application code.
- Request tasks omit `duration`. Pipe tasks require an integer duration in
  minutes from `1` through `43200`.
- Set `runtime.maxRunningTimeSec` deliberately for the workload. The schema
  permits omission, but an implicit runtime default is rarely a sound design
  choice.
- Declare streaming in the card's top-level `streams` object, never inside
  `capabilities`. Request-task streaming is consumer opt-in, so handlers guard
  `ctx.hasStream` / `ctx.has_stream` before creating a stream.
- Consume event streams with `stream.events()` and byte streams with
  `stream.bytes()`. Use low-level `stream.inbound` only when raw envelope
  metadata is required.
- Close task sessions and destroy consumer clients when finished so transports
  unsubscribe cleanly.

## Common workflows

### Modify an existing agent

1. Read the card, handler, and task-relevant reference.
2. Change the implementation and keep IO/stream declarations in sync.
3. Run project tests and `blocks check`.
4. Tell the user whether the change is local only. If it should go live, provide
   `blocks publish` for an already published agent or `blocks register` for an
   agent that should remain private, followed by `blocks run` to restart the
   provider.

### Deploy code that has no card

1. Identify the real handler and its language.
2. Read the Agent Card Schema and relevant language reference.
3. Draft a card from the handler's actual input and output behavior.
4. Show the proposed identity, task kinds, IO, streams, runtime settings, and
   billing mode to the user before writing metadata that requires product
   judgment.
5. Write the approved card, then run `blocks check`.

### Call an agent from a script or service

1. Read the language reference and use `TaskClient`; do not hand-roll the wire
   protocol.
2. Provide `BLOCKS_API_KEY` to the consumer process.
3. Pass the agent's registered `billingMode`, the registered `agentName`, and
   request parts matching the agent card.
4. Register event/stream handlers before awaiting terminal state, then close the
   session and destroy the client.

## Completion check

Before reporting success, verify that:

- the correct project and deployment were used;
- `agent-card.json` passes the current schema through `blocks check`;
- handler behavior and IO/stream declarations agree;
- no secret was added to source or browser code;
- relevant project tests passed, or any unrun check is named;
- the user is told clearly whether the result is only local, registered,
  published, running, and/or tested end to end.

## References

[blocks-getstarted]: https://config.blocks.ai/GETSTARTED.md
[Agent Card Schema]: https://config.blocks.ai/references/agent-card.schema.json
[Agent Card Reference]: https://config.blocks.ai/references/agent-card-reference.md
[IO Schema Reference]: https://config.blocks.ai/references/io-schema-reference.md
[Node Reference]: https://config.blocks.ai/references/node-reference.md
[Python Reference]: https://config.blocks.ai/references/python-reference.md
[Agent Development Guide]: https://config.blocks.ai/references/agent-development-guide.md
