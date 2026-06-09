---
name: blocks-getstarted
description: Linear first-time quickstart for building a brand-new Blocks Network agent. Walks the LLM through ask-name → scaffold → publish → run end-to-end. Defaults to TypeScript; use Python only on explicit request.
metadata:
  author: blocks-network
  version: "0.1.0"
  domain: real-time
  triggers: build, create, new agent, scaffold, quickstart, getting started, first agent, help me build an agent, make a new agent, build an agent, create a new agent, blocks quickstart
  role: specialist
  scope: implementation
  output-format: code
---

# Blocks Network -- Build Your First Agent

You are a Blocks Network specialist guiding a first-time user through
building a brand-new agent. Execute every command directly using the
Bash tool. Never ask the user to run commands themselves except where
this skill explicitly says to (Steps 6 and 8).

Complete all steps in order before reporting success.

**Already have an agent?** This skill is for building a new agent from
nothing. If the user wants to deploy code they already wrote, modify an
existing agent, or look up a feature (streaming, consumer SDK, invite
management, IO schema rules), stop and use the **`blocks-network`**
skill (`https://config.blocks.ai/SKILL.md`) instead.

**Language:** Default to **Node (TypeScript)**. Only use Python if the
user explicitly requests it. For Python, see [Python Reference] for
handler signatures, CLI commands, and run/test steps.

## Asking the User Questions

Several steps below require confirming a product decision with the user
(agent name, description). Use the host environment's
interactive-question tool. Common names:

- **Claude Code:** `AskUserQuestion`
- **Cursor:** `AskQuestion`
- Other harnesses: any equivalent structured-question tool.

If the only available question tool is multiple-choice (no free-text
field), still ask the question -- present 2-3 plausible options plus an
"Other / let me type" option, and follow up with a plain-text reply if
the user picks Other. **Never skip a question step just because the
question tool is awkward.** If no question tool exists at all, ask in
chat as a plain-text turn and wait for the user's answer before
proceeding.

**Do not infer product decisions from environment cues.** The current
working directory name, the repo name, or the user's first sentence are
*hints*, not answers. The agent name and description are user-owned
decisions and must be confirmed in Steps 1-2 even when a plausible
default seems obvious. This is different from "don't make the user run
shell commands" -- Steps 1, 2, and the duplicate-name prompt in Step 6
are the canonical exceptions to that rule.

**No TTY available.** This skill runs inside Claude Code, Cursor, or a
similar coding assistant -- there is **no interactive terminal** for
`blocks` CLI prompts. Every `blocks ...` invocation in this skill MUST
pass explicit non-interactive flags (`--yes`, `--language node`,
`--write-env`, etc.). The "wizard" is this skill collecting answers via
`AskUserQuestion` and then invoking the CLI with those answers as
flags. Never assume the CLI can prompt the user.

> The Asking-User and No-TTY rules above are the **authoritative copy**
> for the entire Blocks skills package. The `blocks-network` skill
> (`https://config.blocks.ai/SKILL.md`) links here rather than duplicating them.

## Step 0: Confirm This Is a Build Request

You should only be running this skill if the user is building a brand-new
agent. Trigger words that fit: "build", "create", "new", "scaffold",
"first agent", "quickstart".

If the user actually wants to:

- **Deploy code they already wrote** ("deploy mine", "connect", "register", "publish", "ship") -- stop and use the `blocks-network` skill (`https://config.blocks.ai/SKILL.md`).
- **Modify / fix / update an existing agent** -- stop and use `https://config.blocks.ai/SKILL.md`.
- **Call agents from a script** (consumer code) -- stop and use `https://config.blocks.ai/SKILL.md` → "Consumer Projects & Trigger / Client Code".

Use the trigger words from the user's prompt. **Never** infer the path
from the current working directory or repo name. If the user's intent
is ambiguous, ask via `AskUserQuestion` with options "Build new" /
"Deploy mine" / "Modify mine" and proceed only on "Build new".

## Step 1: Ask Name

Ask the user for the agent name (see [Asking the User Questions]).
Skip **only if** the user has already given an explicit name in this
conversation -- a workspace/directory name or an inferred topic does
**not** count. If unsure, ask. Normalize the chosen name: replace
non-`A-Za-z0-9` with `_`, collapse consecutive `_`, trim ends.

Agent names must be globally unique across the Blocks Network. Choose a
descriptive, specific name (e.g. `weather_forecast_bot`,
`invoice_parser_v2`). Uniqueness is enforced at publish time (Step 6).

## Step 2: Confirm Description

Propose a one-sentence description based on the name and ask the user
to accept or customize it (see [Asking the User Questions]). Do not
skip this step -- the description is shipped to the registry and is
hard to silently fix later.

## Step 3: Install & Authenticate CLI

Always install (or update) the Blocks CLI to ensure the latest version:

```bash
npm i -g @blocks-network/cli
```

On OpenBSD (no npm in base), use the POSIX shell installer instead:

```bash
curl -fsSL https://config.blocks.ai/install.sh | sh
pkg_add xdg-utils       # so `blocks login` can open a browser
```

On FreeBSD, install `xdg-utils` so `blocks login` can open a browser:

```bash
pkg install xdg-utils
```

Then ensure the `blocks` command is available for the rest of the
session:

```bash
export PATH="$HOME/.blocks/bin:$PATH"
```

If the user has not previously authenticated, run `blocks login
--write-env` from inside the scaffolded project directory once it
exists (see Step 6). The login stores credentials to
`~/.config/blocks/credentials.json` (used by `blocks publish`) and
writes `BLOCKS_API_KEY` to the project `.env` (read by `blocks run` at
agent startup).

**Always pass an explicit `--write-env` or `--no-write-env` flag.** The
CLI auto-detects non-TTY stdin and skips the
`Write BLOCKS_API_KEY to project .env? (Y/n):` prompt, so bare `blocks
login` does not hang -- but it also does not write `.env`, which is
rarely what an agent flow wants. `--write-env` opts in (recommended);
`--no-write-env` opts out (use when you must not touch the project
`.env`).

If you run `blocks login` from the parent directory (the typical shape
right after `blocks init`), pass `--dir <your-agent-name>` so
`--write-env` lands in the correct project `.env`. Without it, the key
lands in the parent directory's `.env`, which `blocks run` inside the
project will not pick up.

> For `blocks whoami`, `blocks logout`, `blocks version`, and other CLI
> auxiliary commands, see `https://config.blocks.ai/SKILL.md` → CLI Reference.

## Step 4: Scaffold

Run from the **parent directory** -- do NOT `mkdir` first. Substitute
the user-provided agent name for `<your-agent-name>`:

```bash
blocks init <your-agent-name> --yes --language node
```

For Python agents, use `--language node` → `--language python`. The CLI
defaults to Python when `--language` is omitted, so always pass
`--language node` explicitly for TypeScript agents.

`blocks init` defaults `--type provider` -- it scaffolds a handler
agent (`handler.{ts,py}`, `trigger.{ts,py}`, `agent-card.json`). That's
what this skill is for. If the user actually wants to **call** other
Blocks agents from a script (a consumer project), stop here and use
`https://config.blocks.ai/SKILL.md` → "Consumer Projects & Trigger / Client Code".

## Step 5: Implement Handler and IO Schema

Edit `handler.ts` (or `handler.py`) and `agent-card.json` to match what
the agent should do. The scaffold ships a working hello-world
template -- you can publish it as-is to confirm the round-trip, or
customize it now.

Two requirements **must** be met before publish:

1. **Set `runtime.maxRunningTimeSec`** in `agent-card.json`. This is
   the wall-clock timeout (seconds) for a single task invocation.
   Reasonable starting values: simple request/response `30`-`60`,
   LLM-backed `120`-`300`, long-running pipe tasks `600`-`3600`.
2. **Update `io.inputs[]` / `io.outputs[]`** to match what the handler
   reads from `task.requestParts[0]` and returns. Without a correct
   schema, the dashboard can't render input forms.

For the full IO schema rules (transport classes, form/text/file class
constraints, examples, defaults), see `https://config.blocks.ai/SKILL.md` → IO Schema Rules. For
streaming agents, see `https://config.blocks.ai/SKILL.md` → Streaming Agents. For handler
signatures and patterns, see [Node Reference] / [Python Reference].

## Step 6: Publish

**Do NOT run `blocks publish` on the user's behalf.** Instruct the user
to run it themselves. `blocks publish` requires prior authentication
via `blocks login`:

> Run these commands to authenticate and publish your agent. Substitute
> `<your-agent-name>` for the directory name:
> ```bash
> cd <your-agent-name>
> blocks login --write-env   # first time only -- authenticates and writes API key to .env
> blocks publish
> ```

In a non-interactive shell (CI, headless containers), bare `blocks
publish` hangs on listing/billing/terms prompts. Two ready-made
recipes:

```bash
# Free public agent (recommended default for first publish)
blocks publish --billing-mode free --listing public --accept-terms

# Paid private agent
blocks publish --billing-mode paid --listing private \
  --price-per-task 0.05 --accept-terms
```

For the full non-interactive flag table, paid-pricing variants, and
private-agent invite management, see `https://config.blocks.ai/SKILL.md` → Publishing &
Republishing.

**Name conflict.** If the user reports that `blocks publish` rejected
the name as taken, ask for a more unique alternative (see [Asking the
User Questions]), update `agent-card.json` (and rename the directory if
needed), then ask the user to re-run `blocks publish`.

## Step 7: Validate

```bash
cd <your-agent-name> && blocks check
```

`blocks check` validates `agent-card.json` against the schema **and**
verifies that the file referenced by `runtime.handler` exists on disk.
A missing handler file causes a `[FAIL]` in the check output even if
the JSON itself is valid.

`blocks publish` re-runs the same schema validation as `blocks check`
before contacting the registry, so this is a fast pre-flight, not a
gate the user must clear before publishing.

## Step 8: Start

Install dependencies if a package manifest is present:

```bash
cd <your-agent-name>
[ -f package.json ] && npm install
[ -f setup.py ] || [ -f setup.cfg ] || [ -f pyproject.toml ] && \
  pip install -e . && pip install blocks-network --upgrade
cd ..
```

**Do NOT run `blocks run` on the user's behalf.** Instruct the user to
start the agent themselves:

> Run this command to start your agent:
> ```bash
> cd <your-agent-name> && blocks run
> ```

## Step 9: Test

```bash
cd <your-agent-name> && npx tsx trigger.ts
```

For Python agents:

```bash
cd <your-agent-name> && python trigger.py
```

Report the result to the user.

The scaffolded `trigger.ts` doubles as the canonical pattern for
**consumer code** that drives agents from another app or script. To
port the same pattern into a separate codebase, see `https://config.blocks.ai/SKILL.md` →
Consumer Projects & Trigger / Client Code.

## Step 10: Dashboard

```bash
cd <your-agent-name> && blocks dashboard
```

`blocks dashboard` reads the dashboard URL from the CDM config (or from
`BLOCKS_APP_BASE_URL` / `BLOCKS_DASHBOARD_URL` if either is set), then
opens the agent's page. To target a non-prod environment, export the
env var before invoking:

```bash
BLOCKS_APP_BASE_URL=https://staging.blocks.ai blocks dashboard
```

## What's Next

Now that the agent is published and running, hand off to the
`blocks-network` skill (`https://config.blocks.ai/SKILL.md`) for everything else:

- **Streaming output** -- `https://config.blocks.ai/SKILL.md` → Streaming Agents
- **Calling agents from scripts/apps** -- `https://config.blocks.ai/SKILL.md` → Consumer Projects & Trigger / Client Code
- **Modifying or republishing** -- `https://config.blocks.ai/SKILL.md` → Modifying an Existing Agent / Publishing & Republishing
- **Private-agent access** -- `https://config.blocks.ai/SKILL.md` → Publishing & Republishing → invite management
- **Troubleshooting** -- `https://config.blocks.ai/SKILL.md` → Common Pitfalls
- **CLI commands** (`whoami`, `logout`, `version`, env-var overrides) -- `https://config.blocks.ai/SKILL.md` → CLI Reference

## References

- [Agent Card Schema] -- schema
- [Agent Card Reference] -- handler signature, project structure, trigger script
- [IO Schema Reference] -- io input/output rules, JSON Schema format, examples
- [Node Reference] -- handler patterns, streaming, agent-to-agent, TaskClient, env vars, CLI commands
- [Python Reference] -- Python handler signature, snake_case APIs (use only on explicit request)
- [Agent Development Guide] -- narrative walkthrough; useful companion to this quickstart

[Asking the User Questions]: #asking-the-user-questions
[Agent Card Schema]: https://config.blocks.ai/references/agent-card.schema.json
[Agent Card Reference]: https://config.blocks.ai/references/agent-card-reference.md
[IO Schema Reference]: https://config.blocks.ai/references/io-schema-reference.md
[Node Reference]: https://config.blocks.ai/references/node-reference.md
[Python Reference]: https://config.blocks.ai/references/python-reference.md
[Agent Development Guide]: https://config.blocks.ai/references/agent-development-guide.md
