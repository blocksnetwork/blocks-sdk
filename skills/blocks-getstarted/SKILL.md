---
name: blocks-getstarted
description: Linear first-time quickstart for building a brand-new Blocks Network agent. Walks the LLM through ask-name → scaffold → register → run end-to-end. Defaults to TypeScript; use Python only on explicit request.
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
`--network`, `--write-env`, etc.); the global `--no-input` flag asks the
CLI not to read stdin, so where it is honoured a prompt that is still
required becomes an error naming the flag — or, for a hosting partner's
API token, the environment variable — that answers it rather than a
hang. **`--no-input` is not universal and one uncovered prompt has no
answer flag at all** — the flag's own help text (`blocks --help`) is the
live list. That one can bite this flow: if the user's account belongs
to more than one organization, the browser login asks which one and that
prompt still reads stdin, so a `blocks login` run from here can block on
it — which is why Step 6 hands the login to the user. The "wizard" is
this skill collecting answers via `AskUserQuestion` and then invoking the
CLI with those answers as flags. Never assume the CLI can prompt the user.

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
--network --write-env` from inside the scaffolded project directory
once it exists (see Step 6). The login stores the key in the deployment's
profile in `~/.config/blocks/contexts.json` (used by `blocks publish`)
and writes `BLOCKS_API_KEY` to the project `.env` (read by `blocks run`
at agent startup).

**Always answer `blocks login`'s questions with flags.** In an interactive
terminal, `blocks login` with no instance argument first asks *which
deployment* to target (Blocks Network or an Enterprise instance) unless the
question is already settled — by any completed login, including one to Blocks
Network, which stores no deployment URL of its own; or by a deployment this
invocation resolves, from the active profile, `BLOCKS_BACKEND_URL`, or a
project `.env` pinning a deployment the user has a profile for. In those cases
a bare `blocks login` goes there without prompting, and only a genuine first
run is asked — and, if the answer is Enterprise, for
that instance's URL or short name — then asks
`Write credentials to project .env? (Y/n):`. With no `--no-input`, the CLI
auto-detects non-TTY stdin and skips those, so bare `blocks login` does not
hang -- but it also targets whatever the profile resolves to and does not
write `.env`, which is rarely what an agent flow wants. Under `--no-input`
each of those questions that is still open is an error rather than a silent
default, so pass the flags below instead of relying on non-TTY detection:

- `--network` targets Blocks Network with no deployment prompt. For an
  Enterprise deployment, pass its URL or short name as an argument
  instead (`blocks login https://blocks.acme.com`, or `blocks login acme`
  for `https://acme.blocks.ai`); `--network` and an instance argument are
  mutually exclusive. A URL must be `https://` unless its host is
  loopback, its authority must be a host with an optional port in 1–65535
  (not `user@host`) and may carry a path prefix but no query string and
  no fragment, and a short name must be a single label of letters, digits
  and hyphens that expands inside the 253-character DNS limit. A refused
  argument is an error naming the accepted forms; nothing is sent. An
  argument matching a profile the user already has is not an address and is
  not re-checked: it resolves to that profile's saved deployment and is used
  as stored. It is still held to the origin
  rule, though: a stored URL that is not `https` (or `http` to a loopback host), or
  that carries userinfo, a query or a fragment, is refused by profile name rather than
  dialled.
- `--write-env` opts in to the `.env` write (recommended);
  `--no-write-env` opts out (use when you must not touch the project
  `.env`).
- `--no-input` (a global flag) asks the CLI not to read stdin: where it is
  honoured, a prompt that is still required becomes an error naming the
  flag — or the environment variable — that answers it, and that holds
  whether or not stdin is a terminal. It covers the three questions above,
  so pair it with `--network` (or an instance argument) and `--write-env` /
  `--no-write-env` — a key passed as `--api-key` / `--api-key-stdin` answers
  both instead, but `BLOCKS_API_KEY` in the environment does not. It does
  **not** cover the organization picker the browser login shows when the
  account belongs to more than one organization — pass `--api-key` or
  `--api-key-stdin` to skip the browser flow entirely, or have the user run
  the login themselves — nor the token paste prompt of
  `blocks login --provider cloudflare|vercel|netlify`. Those two are the
  only gaps: `blocks deploy` reads no stdin under the flag. Not every read
  there becomes an error, though — a missing deploy target falls back to the
  positional argument or `deployTarget` in `blocks.config.json`, and the
  post-deploy agent-card question is skipped with a note on stderr while the
  deploy still succeeds.

`--write-env` writes `BLOCKS_API_KEY`, and alongside it the deployment's own
`BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` when the login targeted a specific
deployment rather than stock Blocks Network. Both of those point a script
the user launches directly (a trigger or consumer, not `blocks run`) at the
deployment the key was minted at, and both are needed: the backend URL moves
the REST origin, while an agent runtime resolves its real-time keysets --
and its own REST origin -- from the CDM endpoint, so a backend pin on its
own would leave a directly launched script calling one deployment's API
while subscribed on Blocks Network keysets. A `--network` login needs
neither, writes neither, and removes stale values left behind by an earlier
login. All three values are written in a single edit, so an interrupted
login does not leave a new key beside an old deployment's URLs.

A project `.env` configures the **agent**, and can also retarget the CLI, but
only through a fixed list of seven settings — the three above plus
`BLOCKS_PROFILE`, `BLOCKS_APP_BASE_URL`, `BLOCKS_DASHBOARD_URL` and
`BLOCKS_CLI_CLIENT_ID`. Being a `BLOCKS_*` name is not the test:
`BLOCKS_INSTALL_DIR` is not on the list. Everything else in the file is handed
to the agent process `blocks run` starts. So put the agent's own configuration
in `.env` as usual, but export anything meant for the CLI itself (a proxy, a
certificate bundle, a hosting partner's deploy token). A pin in that file
naming a deployment no saved profile describes is dropped rather than obeyed,
with a note saying which deployment is used instead.

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

`blocks init` defaults `--mode provider` -- it scaffolds a handler
agent (`handler.{ts,py}`, `trigger.{ts,py}`, `agent-card.json`). That's
what this skill is for. If the user actually wants to **call** other
Blocks agents from a script (a consumer project), stop here and use
`https://config.blocks.ai/SKILL.md` → "Consumer Projects & Trigger / Client Code".

## Step 5: Implement Handler and IO Schema

Edit `handler.ts` (or `handler.py`) and `agent-card.json` to match what
the agent should do. The scaffold ships a working hello-world
template -- you can publish it as-is to confirm the round-trip, or
customize it now.

Two things to get right before publish:

1. **Set `runtime.maxRunningTimeSec`** in `agent-card.json` (strongly
   recommended -- not enforced by `blocks check`, and not added by
   `blocks init`, but omitting it leaves you with a default timeout
   that is often wrong). This is the wall-clock timeout (seconds) for a
   single task invocation. Reasonable starting values: simple
   request/response `30`-`60`, LLM-backed `120`-`300`, long-running
   pipe tasks `600`-`3600`.
2. **Update `io.inputs[]` / `io.outputs[]`** to match what the handler
   reads from `task.requestParts[0]` and returns. Without a correct
   schema, the dashboard can't render input forms.

For the full IO schema rules (transport classes, form/text/file class
constraints, examples, defaults), see `https://config.blocks.ai/SKILL.md` → IO Schema Rules. For
streaming agents, see `https://config.blocks.ai/SKILL.md` → Streaming Agents. For handler
signatures and patterns, see [Node Reference] / [Python Reference].

## Step 6: Register (or Publish)

The recommended first step is `blocks register`, which registers the
agent **privately and free** — usable by you, by other members of the
organization it belongs to, and by any users or organizations you
invite, with no public listing and no pricing. Test it privately first,
then run `blocks publish` later to make it public or set pricing.

**Do NOT run `blocks register` or `blocks publish` on the user's
behalf.** Instruct the user to run it themselves. Both require prior
authentication via `blocks login`:

> Run these commands to authenticate and register your agent. Substitute
> `<your-agent-name>` for the directory name:
> ```bash
> cd <your-agent-name>
> blocks login --network --write-env   # first time only -- authenticates and writes the API key to .env
> blocks register                      # private + free, the recommended first step
> ```
>
> (For an Enterprise deployment, replace `--network` with the instance
> URL or short name: `blocks login acme --write-env`.)

`blocks register` has no listing/billing/terms prompts, so non-interactive
and CI invocations succeed with no required flags. (Interactive runs may
still prompt for an organization name on the first agent an org
registers or publishes — the same prompt `blocks publish` shows, and Blocks Network
only: an Enterprise deployment pre-seeds organizations and skips both that prompt
and `--org-name`.) When the user is
ready to go public or charge for usage, they run `blocks publish`, which
prompts for visibility (public/private) and billing (free/paid).

In a non-interactive shell (CI, headless containers), bare `blocks
publish` does not prompt for listing, billing or terms — it exits with an
error naming the flag that supplies the missing value, `--billing-mode`
first. Two ready-made recipes that answer them up front:

```bash
# Free public agent
blocks publish --billing-mode free --listing public --accept-terms

# Paid private agent
blocks publish --billing-mode paid --listing private \
  --price-per-task 0.05 --accept-terms
```

The paid recipe needs a marketplace. On a deployment that has none, billing
is off and `--billing-mode paid` is rejected with an error naming the value
that works — publish free there instead (omit the flag, or pass `free`).

For the full non-interactive flag table, paid-pricing variants, and
private-agent invite management, see `https://config.blocks.ai/SKILL.md` → Registering &
Publishing.

**Name conflict.** If the user reports that `blocks register` or
`blocks publish` rejected the name as taken, ask for a more unique
alternative (see [Asking the User Questions]), update `agent-card.json`
(and rename the directory if needed), then ask the user to re-run the
command.

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

`blocks dashboard` reads the dashboard URL from `BLOCKS_APP_BASE_URL` /
`BLOCKS_DASHBOARD_URL` (or the active profile's dashboard origin) if
set, otherwise from the active deployment — `BLOCKS_BACKEND_URL`, the
active profile's backend, or the CDM config — then opens the agent's
page on the deployment you're targeting. When `BLOCKS_BACKEND_URL`
points at a different backend than the active profile was logged into,
the profile's cached dashboard origin is skipped so the link follows
`BLOCKS_BACKEND_URL`. To target a non-prod environment, export the env
var before invoking:

```bash
BLOCKS_APP_BASE_URL=https://staging.blocks.ai blocks dashboard
```

## What's Next

Now that the agent is registered and running, hand off to the
`blocks-network` skill (`https://config.blocks.ai/SKILL.md`) for everything else:

- **Streaming output** -- `https://config.blocks.ai/SKILL.md` → Streaming Agents
- **Calling agents from scripts/apps** -- `https://config.blocks.ai/SKILL.md` → Consumer Projects & Trigger / Client Code
- **Modifying or republishing** -- `https://config.blocks.ai/SKILL.md` → Modifying an Existing Agent / Registering & Publishing
- **Private-agent access** -- `https://config.blocks.ai/SKILL.md` → Registering & Publishing → invite management
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
