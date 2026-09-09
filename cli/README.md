# Blocks CLI

The Blocks CLI is the single canonical CLI for the Blocks Network
project. It is a standalone Go binary -- it does not depend on the
Node SDK or npm.

## Commands

| Command | Description |
|---------|-------------|
| `blocks init` | Scaffold a new agent project (Node or Python) |
| `blocks check` | Validate `agent-card.json` and handler file |
| `blocks login [instanceUrl\|shortName]` | Authenticate and store credentials for future commands. An optional instance URL targets a specific deployment (the CLI auto-discovers whether it is enterprise). Short names like `acme` target `https://acme.blocks.ai`; a name matching an existing profile reuses that profile's deployment. Pass `--network` to target Blocks Network without prompting. In a terminal, with no argument and no deployment resolved for this invocation at all, prompts for which one to target. See [What `blocks login` accepts as a deployment](#what-blocks-login-accepts-as-a-deployment). |
| `blocks register` | Register an agent privately and free — the recommended first step (requires prior `blocks login` or `--api-key`) |
| `blocks unregister` | Remove an agent from the deployment you are targeting, the inverse of `blocks register`. Requires `--yes` in non-interactive use (CI, scripts). |
| `blocks publish` | Publish an agent to the network, choosing public/private and free/paid (requires prior `blocks login` or `--api-key`) |
| `blocks run` | Start an agent (delegates to the SDK's `blocks-run` binary for Node, venv Python `-m blocks_network` for Python) |
| `blocks logout` | Clear the selected profile's cached org keys and strip `BLOCKS_API_KEY` from the project `.env`. Keeps the profile's deployment target and says so, so a later `blocks login` returns to it; use `blocks profile remove` to forget the deployment. Does not revoke the key on the server. |
| `blocks profile` | Manage deployment profiles — `list`, `use <name>`, `rename <old> <new>`, `remove <name>` |
| `blocks whoami` | Display current authenticated identity |
| `blocks upgrade` | Upgrade the CLI to the latest release |

`blocks run` is the canonical way to start any agent. It detects the
project language from the handler extension or project files and
delegates to the appropriate SDK runner:

- **Node:** walks up the directory tree for the SDK's `blocks-run` bin in
  `node_modules/.bin`, then for the built runner inside a `blocks-sdk`
  workspace. Falls back to `blocks-run` on PATH.
- **Python:** walks up the directory tree to find `.venv/bin/python`,
  then runs `python -m blocks_network`. Falls back to `blocks-run` on
  PATH if no venv is found.

It also injects, for the process it starts, the API key for the deployment
you are targeting plus that deployment's `BLOCKS_BACKEND_URL` and
`BLOCKS_CDM_URL` — for **any** named deployment, enterprise or not, since
every deployment serves its own real-time keysets. Stock Blocks Network is
left unpinned, because the SDK's own defaults already resolve it. The
injection is a default, not an override: a value the child's environment (or the
project `.env`) already carries non-empty wins over the injected one, so an
explicitly-set `BLOCKS_CDM_URL` that disagrees with the target is what the
delegated process reads.

### Project modes

`blocks init` can scaffold three kinds of projects via the `--mode` flag:

- `--mode provider` (default): an agent handler project. Produces
  `handler.{ts,py}`, `trigger.{ts,py}`, and `agent-card.json`.
  Use `blocks register` (private + free, the recommended first step) or
  `blocks publish` (to choose public/paid) to deploy, and `blocks run` to run.
- `--mode consumer`: a script that calls other agents via `TaskClient`.
  Produces `index.ts` / `main.py`. Run with `npm run start` or
  `python main.py`.
- `--mode webapp`: a static page pre-wired with the Blocks embed-auth
  widget for one or more named agents. Pass `--agent <name>` (repeatable)
  to select which agents the page talks to. Scaffolded projects carry a
  required `backendBaseUrl` in `blocks.config.json` — the backend API
  origin the deployed page calls at runtime. It is resolved (highest
  precedence first) from `--backend-url`, the `BLOCKS_BACKEND_URL` env
  var, your active profile's backend (`blocks profile use <name>`), the
  build-time default baked into packaged/enterprise builds, and finally
  the asset host (`--blocks-base-url`). When `--blocks-base-url` is unset,
  the asset host mirrors the resolved backend origin above; it only defaults
  to `https://app.blocks.ai` for stock users.

Examples:

```bash
blocks init my_agent                         # provider (default)
blocks init my_consumer --mode consumer      # consumer, prompt for language
blocks init my_consumer --mode consumer --language python --yes
blocks init my_ui --mode webapp --agent echo # webapp wired to the echo agent
```

## Profiles & deployments

A **profile** is a named deployment target (Blocks Network or an Enterprise
instance) with its own base URL, branding, and per-org API-key cache. The
stock `blocks-network` profile is the built-in default and is present even before
a first login.

```bash
blocks profile list                       # show profiles (active one marked)
blocks profile use acme                    # switch the active profile
blocks profile rename localhost:3001 dev   # rename a profile (data preserved)
blocks profile remove acme                 # delete a profile
```

Logging in to an enterprise deployment creates/updates a profile. By default
the profile is named after the deployment — its host, plus any path prefix, so two
tenants on one host (`https://host/tenant-a` and `/tenant-b`) get their own profiles
rather than sharing one that describes one tenant while holding the other's keys. Pass
`--profile <name>` to store it under a custom name instead; that names the destination
outright, so pointing it at a deployment the profile does not describe retargets it and
discards the organization keys cached for the old one, with a note:

```bash
blocks login https://blocks.acme.com                   # profile "blocks.acme.com"
blocks login https://blocks.acme.com --profile acme    # profile "acme"
```

### What `blocks login` accepts as a deployment

An argument that spells out an address is where an API key is about to be
sent, so it is checked before anything is transmitted:

- **Full URL** — `https://` is required, and the authority must be a host
  with an optional port in the range 1–65535. `http://` is accepted
  **only** for `localhost`, `127.0.0.1` and `[::1]`, which is what keeps a
  credential from going out in clear text to anywhere but your own machine. A URL whose
  authority is `user@host` is refused, because the host it reaches is not
  the one it appears to name. A custom port and a path prefix are both
  preserved; a query string or a fragment is refused, because this value is
  an origin the CLI appends endpoint paths to and neither can be part of
  one — `https://host?x=1` would send every request to a path no
  deployment serves.
- **Bare host** — `blocks.acme.com` is accepted and `https://` is
  prepended. Anything that would stop meaning a host once a scheme is in
  front of it (a `/`, `@`, `?` or `#`, whitespace, control characters) is
  refused rather than guessed at. The URL it builds is then held to the
  same check the full-URL form goes through, so the 1–65535 port range
  applies here too: `blocks.acme.com:99999` is refused now rather than
  failing at the first request.
- **Short name** — must be a single DNS label (letters, digits and
  hyphens, not starting or ending with one), which is then expanded to
  `<name>.blocks.ai`. The expanded hostname is capped at the 253-character
  DNS limit; a name that would overflow it is refused with the reason.
- **An existing profile name** takes precedence over both host forms, so
  an alias for a custom domain keeps working after the first login. This
  form is not an address and the checks above do not apply to it: it
  resolves to the URL that profile already stores and uses it as saved,
  because the profile exists only because a login already reached that
  deployment. A full URL is still checked — the scheme form is matched
  first, even if a profile happens to be named after it.

A refused argument is an error naming the accepted forms. Nothing is sent
and no profile is created.

### Selecting a profile per command

Every command resolves which profile to use in this order:

1. `--profile <name>` — a persistent flag accepted by any command
   (e.g. `blocks --profile acme publish`, `blocks --profile acme logout`).
2. `BLOCKS_PROFILE` environment variable.
3. The saved active profile (set by `blocks profile use`).
4. The default `blocks-network` profile.

### When `BLOCKS_BACKEND_URL` overrides your profile

`BLOCKS_BACKEND_URL` outranks whichever profile is selected and decides where
commands actually go. Exporting it in your shell, or setting it in your CI job, is
the supported way to point a scripted or headless run at a deployment. The CLI
also loads a `.env` from the current directory before any command runs, without
overriding variables the environment already carries and skipping empty
assignments — taking only its own `BLOCKS_*` settings from that file, and with
restrictions on the two targeting values among them. Both are described below.

`blocks login --write-env` is what normally leaves such a pin behind. It writes
`BLOCKS_API_KEY`, and — when the login targeted a specific deployment — that
deployment's `BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` alongside it. All three
describe the deployment the login actually authenticated against, so a completed
write does not leave a key minted at one deployment beside the URLs of another,
and all three are applied as a single rewrite.

That rewrite leaves one uncommented assignment of each of those variables in the
file — under any spelling of the name, because for a variable whose name
uppercases to `BLOCKS_*` the writer treats `blocks_api_key` and `BLOCKS_API_KEY`
as the same variable, and `blocks logout` removes both. With one assignment in the
file, a loader that takes the first and one that takes the last do not disagree
about it, and neither does a case-insensitive process environment on Windows. Two
things that statement does not say. Outside `BLOCKS_*` each spelling is its own
variable to the writer, so "assigned once" is per spelling for an application
variable. And the fold applies on every platform, so on Unix a `blocks_api_key`
line you wrote by hand is a line `blocks login` will overwrite and `blocks logout`
will delete — a cross-platform behaviour change, not a Windows-only fix. It does
not make the CLI read that spelling as a variable on Unix; it decides which line
the writer edits.

A value that would not survive the round trip — one
containing a line break or a NUL byte, which the loader would read back as a
second assignment — is refused by name, and the whole rewrite is abandoned rather
than writing the acceptable half of it. A single quote is refused for the same
reason: no way of escaping one is read identically by a shell and by both dotenv
parsers.

Everything else is written so that `source .env` cannot run it. A value is left
bare only when every byte is inert (`A-Za-z0-9` and `_-./:@+,%=`); anything else
is single-quoted, where a POSIX shell expands nothing — so a `$(...)`, a backtick
or a `;` that a deployment put inside a key or a URL is data, and the variable
holds exactly the bytes the deployment sent. That covers the lines this CLI wrote,
in `sh`/bash/zsh, Node `dotenv` and `python-dotenv`. It does not cover lines you
wrote yourself: a hand-written `FOO=$(curl ...)` in the same file still runs on
`source`. And it says nothing about whether the value is *correct* — a hostile
deployment can still send a wrong URL; it just cannot execute.

If `.env` is a symlink, the write goes through the link rather than replacing it —
replacing the name would leave the file it pointed at holding the old key while the
command said the key was written — and the link is followed only when two things
hold. The target has to stay **inside the project**: the repository the `.env` sits
in, or that directory itself when it is not a checkout, so `.env -> ~/.env` is
refused even though it is named `.env`. And the link has to **name the file it
points at**: the target's file name must be the link's own name, or a plain `.env`,
with no `.git` component anywhere in the path. So a shared file above a package
directory (`packages/api/.env -> ../../.env`) keeps working, while
`.env -> README.md`, `.env -> .env.example` and anything in `.git/` are refused —
naming both ends and writing nothing — rather than having a credential written into
them. A link to a differently-*named* shared file is refused with them; rename the
link, or run the command where the real file is. A link that resolves to nothing is
refused too, because treating it as a missing file is what would destroy it. Every
write is `0600`, whatever mode the target had, so a credential does not inherit a
world-readable file's permissions — and a `.env` you deliberately kept
group-readable is narrowed, which one `chmod` undoes. A stock Blocks Network login
(`--network`) needs no targeting: it writes neither URL, and removes stale ones
left by an earlier login.

Both URLs matter for anything you launch yourself rather than through
`blocks run` — a trigger or consumer script — and they steer different halves of
it. `BLOCKS_BACKEND_URL` moves the REST origin, and only for a consumer; an agent
runtime resolves its real-time keysets, and its own REST origin, from the CDM
config endpoint named by `BLOCKS_CDM_URL`, which unset falls back to a hardcoded
default serving Blocks Network keysets. So a backend pin on its own leaves a
directly-launched script talking REST to one deployment while subscribed to
another's keyset. Every deployment serves its own CDM payload at
`<origin>/api/v1/cdm`, which is exactly the value `--write-env` writes.
`blocks run` supplies both to the process it delegates to whenever the invocation
targets a named deployment — enterprise or not — so an agent started that way
needs neither in its `.env`. It supplies them as defaults rather than overrides:
a non-empty `BLOCKS_BACKEND_URL` or `BLOCKS_CDM_URL` the child already carries
wins, so if you set one explicitly to something that disagrees with the target,
that is what the delegated process reads.

#### What a project `.env` can and cannot change about the CLI

A project `.env` configures **your agent**. It can also change where the CLI
points and which credential it sends, but only through a fixed list: from that
file the CLI imports the seven settings it consumes itself, and nothing else.

`BLOCKS_API_KEY`, `BLOCKS_BACKEND_URL`, `BLOCKS_CDM_URL`, `BLOCKS_PROFILE`,
`BLOCKS_APP_BASE_URL`, `BLOCKS_DASHBOARD_URL`, `BLOCKS_CLI_CLIENT_ID`.

Nothing else in the file is set into the CLI's own process — and nothing else is
discarded either: `blocks run` merges the whole file into the environment of the
agent process it starts, so a `PYTHONPATH` for a `src` layout, a `NODE_OPTIONS`
heap size, and every application variable your handler reads all keep arriving
from `.env` exactly as before. They simply do not configure the CLI. That is the
point: a `.env` travels with a cloned repository, and nothing a repository ships
can change the transport the CLI uses, which certificates it trusts, where it
keeps your credentials, or what it executes.

What a cloned `.env` *can* do is point the CLI at a different deployment, since
`BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` are on the list above. So a
file-supplied pin naming a deployment no saved profile describes is not obeyed at
all. The CLI drops it, and prints a note naming the file, the variable and the
deployment it is using instead — a profile exists only because you logged in to
that deployment, which makes the store the record of which origins you have
deliberately trusted with a credential. The withdrawal covers the delegated agent
too, not just the CLI: `blocks run` hands the child nothing that was dropped. Note that being a `BLOCKS_*` name
is not the test — `BLOCKS_INSTALL_DIR`, for instance, is not on the list above.

A few names people commonly put in a `.env` would be puzzling to see ignored, so
when the file supplies one the CLI prints a note on stderr naming the variable
and the file, and pointing at an `export` in your shell as the way to make it the
CLI's own. The note only explains; the list above is what decides:

- **Transport and TLS trust** — `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`,
  `NO_PROXY`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, `GODEBUG`. How the CLI reaches
  the network, whose certificates it believes, and which retired behaviours the
  Go runtime re-enables.
- **State location** — `XDG_CONFIG_HOME`, `HOME`, `USERPROFILE`. Between them
  these move the profile store, the legacy credential store, the deploy-plugin
  directory and the cached deployment configuration, which is what every other
  rule on this page is checked against.
- **Hosting-provider tokens** — `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`,
  `VERCEL_TOKEN`, `VERCEL_TEAM_ID`, `NETLIFY_AUTH_TOKEN`. These choose the
  account your assets are published into.
- **`BLOCKS_INSTALL_DIR`** — the directory `blocks upgrade` writes a freshly
  downloaded binary into.

The hosting-provider tokens are the entry with a day-to-day consequence:
`blocks deploy` resolves them from the environment, so one sitting in a project
`.env` is **not** used — export it instead. That has always been the documented
way to set them for a non-interactive deploy.

The match is case-insensitive throughout, so a lowercase spelling names the same
variable. A note is printed only for an assignment that would otherwise have been
the one in effect: a variable your shell already exports, or an empty assignment,
is not mentioned, because neither was going to be imported anyway.

#### A project `.env` cannot aim a command at an unknown deployment

Both targeting variables are checked when a `.env` in the current directory
supplied them — rather than your own shell or CI job — and the check runs for
**every** command, not only `blocks login`. A value you export wins at both of
these gates and is left alone by them, so scripted and CI use is unaffected. That
precedence belongs to these two gates: naming a target on the `blocks login`
command line drops an ambient `BLOCKS_CDM_URL` that answers for a different
deployment however you set it, as the paragraphs below this list describe.
The two variables are held to related but distinct rules, because they answer
different questions:

- **`BLOCKS_BACKEND_URL`** is honoured when **any saved profile** describes the
  deployment it names — that is, when you have logged in to that deployment at
  some point. It does **not** have to agree with the profile that is currently
  active: disagreeing with the active profile is the whole point of a
  project-local pin, and the pin is what decides where the command goes.
- **`BLOCKS_CDM_URL`** is honoured only when it is the CDM endpoint of the
  deployment the command is *actually* targeting — `<that origin>/api/v1/cdm`.
  When the target is stock Blocks Network there is no such origin to compare
  against, so a file-supplied value is declined there.

Anything else is dropped, and the CLI reports on stderr what it dropped, which
deployment that disagrees with, and the two ways to keep it — log in to the
deployment it names (or pin the deployment that serves that CDM endpoint), or
export the value in your shell.

The reason is provenance. A `.env` arrives with a cloned repository, so it is
weaker evidence of intent than an export, and either variable can move where a
command goes — and, in the case of `blocks login`, where a credential is
transmitted. A profile exists only because someone deliberately authenticated to
that deployment, which is why the profile store is what a backend pin is checked
against.

In a project you logged in from, none of this is visible: `blocks login
--write-env` creates the profile for the deployment it signed in to and writes
that deployment's URLs, so the `.env` it leaves behind names a deployment
the CLI knows and a CDM endpoint that deployment serves. The rule bites on a
`.env` that arrived from somewhere else. Note that the stock `blocks-network`
profile records no deployment of its own, so it vouches for no pin.

A login that names its target **explicitly** goes one step further, because a CDM
payload names both the API origin to use and the OAuth client id to authorize
with, so leaving an ambient `BLOCKS_CDM_URL` in place would let the environment
redefine the deployment you just named:

- An **explicit Network choice** — the `--network` flag, or picking Blocks
  Network at the interactive prompt — unsets an ambient `BLOCKS_CDM_URL` for that
  login, whatever supplied it. Blocks Network's own configuration is the CLI's
  built-in default, so no value named by the environment can be it.
- An **explicit instance** — `blocks login acme`, a bare host, or a full URL —
  unsets one too, unless it is that instance's own CDM endpoint (which is exactly
  what `blocks login <instance> --write-env` leaves behind in that instance's
  project directory). Otherwise a directory pinned to one deployment would
  present that deployment's OAuth client id to the instance you named.

Both say on stderr when they drop a value.

This is the one place a shell-exported value does *not* win. The two gates above
weigh provenance and follow an export; these drops do not, because a choice you
made on this invocation outranks ambient configuration — the same reason an
instance argument wins over `BLOCKS_BACKEND_URL` outright. They apply to that
login process only and leave your shell untouched.

Where an override *is* in force and names a deployment other than the selected
profile's, `blocks profile list`, `blocks whoami`, and `blocks profile use` say
so:

```console
$ blocks profile list
* blocks-network  Blocks Network (default)
  Note: BLOCKS_BACKEND_URL in ./.env overrides this → https://blocks.acme.com
  blocks.acme.com  https://blocks.acme.com
```

The note is attached to the selected profile — the one it is telling you is not
the target — and appears only when the override points somewhere else. It names
the file when the value came from a `.env` in the current directory; a value
exported in your shell is reported without a source, because the two are
indistinguishable once the process has started.

`blocks whoami --json` carries the same information as a `backend_url_override`
field, present only while an override is in force.

To go back to the profile, remove the `BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL`
lines from `./.env`, or unset them in your shell. `blocks profile remove <name>`
does that cleanup for you, and only when the deployment is genuinely being
forgotten: if another profile still describes the same deployment — an alias and
a host slug, as an explicit `--profile` or a rename can produce — the removal forgets a *name*,
not a deployment, and the `.env` is left exactly as it is, because the surviving
profile still targets it. Where it does act, it drops each of the two URLs that
names the removed deployment — the two are judged independently — and the
`BLOCKS_API_KEY` beside them when the backend URL was one of them, since the
backend pin is what records where that key is spent, or when the key is
byte-identical to one only the removed profile had cached (which is how a
`--network` login's key, deliberately left unpinned, is still recognised). A
`.env` whose only match is the CDM endpoint therefore loses that line and keeps
the key. It says which lines it removed, so a forgotten profile does not leave the
directory pointing at a deployment you no longer have credentials for. The cleanup
runs before the profile is deleted, so a cleanup that fails keeps the profile.
Values naming any other deployment are deliberate and are left alone; a `.env`
that exists but cannot be read or rewritten fails the command rather than being
passed over in silence, while no `.env` at all is a silent success.

Which of those lines may go is decided against the profile store as it stands at
the moment the `.env` is rewritten, so a `blocks login` that adds a second name
for the same deployment while the removal is in flight keeps its own pins and key.
The read is late but not locked, so this narrows the window rather than closing it:
two CLI processes rewriting the store or the `.env` concurrently can still lose one
side's update.

`blocks logout` is deliberately the other way round: it removes only
`BLOCKS_API_KEY` and keeps both URLs, because the profile survives and
`blocks login` is meant to return to it. `profile remove` forgets a deployment;
`logout` forgets a credential. That promise is now kept even when the directory's
`.env` pins a deployment the *active* profile does not describe: a later bare
`blocks login` goes to the deployment this invocation resolves — the pin's — with
no prompt, rather than asking again and offering Blocks Network as the default. A
genuine first run, with nothing recording or naming a deployment, is still asked.

### Non-interactive usage

For CI, automation, and scripted use, the prompts on the main flows have flags
that answer them up front:

- `blocks login --network` — target Blocks Network without the deployment prompt
  (or pass an instance URL / short name to target a specific deployment)
- `blocks login --write-env` or `--no-write-env` — answer the .env write prompt
- `blocks unregister --yes` — confirm a destructive removal
- `blocks init --yes` — use all defaults without prompting
- `blocks publish --listing … --billing-mode … --accept-terms` — answer the
  registry prompts. On a deployment with no marketplace, billing is off, so
  `--billing-mode paid` is rejected with an error naming the value that does
  work; publish free instead (omit the flag, or pass `--billing-mode free`).
  Blocks Network still accepts both modes, and both mean what they always did.
  One thing did change there: `blocks publish --billing-mode free` no longer asks
  the deployment for its pricing limits first. Every use of those limits — the
  price prompts, the ceilings on free tasks and free minutes, and the refusals
  that cite them — sits on the paid side, so nothing the request could return can
  change a free publish, and paying for the round trip only slowed it down. An
  interactive `blocks publish` that has not settled the mode makes no request
  either until the mode resolves to paid: the bounds are supplied as a function
  that `CollectPromotionInput` calls at the top of its paid block, which is still
  ahead of the price prompt that renders the permitted range. So choosing free at
  the prompt costs no request, and neither does a publish refused for conflicting
  pricing flags.

**`--no-input`** is the global flag that turns "would prompt" into "fails fast".
Its contract, where it is honoured: a prompt the CLI would otherwise read from
stdin becomes an actionable error naming the flag that answers it, so a missing
answer produces an immediate, self-describing exit rather than a hang. It applies
whether or not stdin is a terminal — asking not to be prompted is enough on its
own.

That last part matters for two `blocks login` decisions that used to fall back to
a silent default off a terminal: which deployment to target, and whether to write
`.env`. Under `--no-input` each is an error naming the flag that answers it
(`--network` or an instance argument; `--write-env` or `--no-write-env`), because
"cannot ask" and "asked not to ask" are different requests and only the first
licenses a guess — silently landing on Blocks Network would authenticate somewhere
you never named. **Without** the flag, a non-TTY run still defaults silently, so
existing CI is unaffected. A credential passed as `--api-key` or
`--api-key-stdin` answers both questions on its own — the deployment stays
whatever the profile and environment resolve to, and `.env` is left untouched — so
that combination needs no extra flag. A `BLOCKS_API_KEY` sitting in the
environment does **not** count as such a credential; pass the flags.

```bash
blocks --no-input login --network --write-env
blocks --no-input register
blocks --no-input publish --listing public --billing-mode free --accept-terms
```

Some refusals name an **environment variable** rather than a flag, because that
is what answers the question: a hosting partner's API token, or the
`credentialEnvVar` a user-defined deploy plugin declares.

**The contract is not universal, and not every prompt has an answer flag.** Some
prompts still read stdin under `--no-input`, and one of those has no flag that
answers it at all — so do not treat the flag as a guarantee that a command
cannot block. The flag names its own current exceptions, and that help text is
kept in step with the code, so it — not this README — is the live list:

```sh
blocks --help | grep -A3 no-input
```

At the time of writing they are: the organization picker shown at the tail of a
browser login when your account belongs to more than one organization, and the
token prompt for `blocks login --provider cloudflare|vercel|netlify`. The first
can be avoided rather than answered — pass `--api-key` / `--api-key-stdin` to
skip the browser flow, and with it the picker; the second *is* the paste prompt,
so run that command without `--no-input` or export the token instead.

`blocks deploy` reads no stdin at all under `--no-input`, but "closed" is not
always "refused" — the five reads take three different shapes. A token prompt is
an error naming the partner's token environment variable (or
`blocks login --provider <partner>`, run once beforehand). A missing target is
**not** an error: it falls back to the positional argument or `deployTarget` in
`blocks.config.json`, and only errors when neither exists. And the post-deploy
agent-card question is **withdrawn**: the card is left unchanged, a note on
stderr names the agent and `--no-card-update`, and the deploy still exits `0`.
That last one is deliberate — the question comes after your bundle has been
uploaded, so failing it would report a live deployment as a failed command and
tell a retry-on-failure script to upload again. Pass `--no-card-update` to state
that intent and silence the note. One thing *is* checked before the upload: a
malformed `--card-path <agent>=<path>` fails immediately with nothing deployed
(a well-formed path that does not exist is still only a warning, after the fact).
One refusal there has no answer at all:
if `web/` was built at `blocks init` time for a different backend than the one
you are now targeting, `--no-input` makes that a hard failure with no override
flag, because the only honest fix is to stop the two from disagreeing — re-run
`blocks init --mode webapp --agent <agent> --backend-url <the backend you are
targeting>`, or select the profile the bundle was built for. Without
`--no-input`, a non-terminal deploy still warns and continues, exactly as before.

### The deployment banner before a state-changing command

`register`, `publish`, `unregister`, `invite send`, `invite revoke` and
`invite accept` print a one-line context banner before they act, so a command
aimed at the wrong deployment is visible before it lands. It names the
deployment, then — only where that is what the operation actually turns on — what
is about to be affected:

| Command | Banner |
|---|---|
| `register`, `publish` | `[deployment / organization]` — the agent is created under one organization |
| `unregister`, `invite accept` | `[deployment]` — the confirmation prompt (or the success line) names the subject |
| `invite send`, `invite revoke` | `[deployment / agent <name> → <grantee>]` — the grantee is the email address, or `org <slug>` |

`invite list` and `invite grants` print none: they change nothing. Every `invite`
subcommand that takes an agent name validates it as a registry name before the
banner is printed, and escapes it into the request path, so the name you are shown
is the name the request uses.

The organization appears only on `register` and `publish` because only there does
it scope the operation. A removal or a grant change is authorized by your rights
over the named agent, so a multi-organization user can act on an agent belonging
to an organization other than the one the key was minted for — naming that
organization would read as a promise about blast radius that it does not make.

On `register` and `publish`, an organization the CLI cannot establish locally is
reported as `organization unknown` rather than guessed at. A key you supplied on
the command line, via stdin or via `BLOCKS_API_KEY` is **not** automatically
unknown: when it matches, byte for byte, a key the target's profile already
cached — the ordinary `login --write-env` shape — the store itself names the
organization. It is unknown only when no local record accounts for the key, or
when the key belongs to a different deployment.

Each part of the banner is escaped before printing — control characters and the
nine explicit bidirectional formatting characters (U+202A–U+202E, U+2066–U+2069),
which reorder the words around them without being control characters at all — so a
name or an argument carrying either does not rewrite or reverse the line you are
checking, or the destructive prompt below it. Ordinary right-to-left script needs
none of those characters and is left as it is.

Escaping is applied print site by print site rather than across the CLI, so read
it as two lists rather than as a property. Escaped: this banner, the destructive
confirmation, the login and targeting notes, the values a deployment returns to
`invite`, the organization row `blocks whoami` prints, the agent name in
`publish`/`register`'s output and success banner, and the top-level error line the
CLI prints when a command fails. Not escaped: profile names, including the
`Profile:` row of `blocks whoami` itself, and the direct output of a few internal
helpers (the registry browser, the `blocks init` wizard, the dev server). Error
text a deployment returns is in between — escaped when it comes out through that
top-level printer, raw when something prints it directly. Nothing is printed when
no credential resolves at all, or when the deployment cannot be named, rather than
a half-empty line.

### Credential storage

Profiles are stored in `~/.config/blocks/contexts.json`
(`$XDG_CONFIG_HOME/blocks/contexts.json` when `XDG_CONFIG_HOME` is exported in
your shell — a project `.env` cannot relocate the store, see
[What a project `.env` can and cannot change about the CLI](#what-a-project-env-can-and-cannot-change-about-the-cli)),
written with `0600` permissions. On first run, a legacy `credentials.json` from an older
CLI is automatically migrated into the default profile.

A store that exists but cannot be read stops the command with an error naming the
file, rather than being treated as though you had no profiles at all. The
distinction matters because that list is what a project pin is checked against: an
unreadable store would make each pin look unknown, including the ordinary one
`blocks login --write-env` leaves in its own directory. Never having logged in is
a different case and stays a silent, legitimate empty.

## Installation

Install the latest release via npm (Linux, macOS, Windows, FreeBSD):

```sh
npm install -g @blocks-network/cli
```

Or via shell script (works on every supported platform, including
FreeBSD and OpenBSD — the installer is POSIX `sh`-compatible, so no
bash is required):

```sh
curl -fsSL https://config.blocks.ai/install.sh | sh
```

### Upgrading

Run `blocks upgrade` to download and install the latest release. The CLI
checks the npm registry for new versions every 2 hours and prints a
notice to stderr when an update is available. Upgrade behavior by install
method:

- **`~/.blocks/bin` (install.sh, `make install`)** — `blocks upgrade`
  replaces the binary in place.
- **npm global (`npm i -g`)** — `blocks upgrade` detects this and
  directs you to run `npm i -g @blocks-network/cli@latest` instead.
- **OpenBSD** — npm packages are not published; use `install.sh`.

Environment variables:

- `BLOCKS_INSTALL_DIR` — override the install directory for `blocks upgrade`.

Files created:

- `~/.blocks/update-check.json` — caches the latest version to avoid
  hitting the registry on every invocation.

On FreeBSD and OpenBSD, install `xdg-utils` so `blocks login` can open
your browser:

```sh
pkg install xdg-utils   # FreeBSD
pkg_add xdg-utils       # OpenBSD
```

---

## Local Development

### Setup

```sh
cp .env.example .env
# Fill in the values for your environment
```

### Run

```sh
set -a && source .env && set +a
go run . login "$BLOCKS_BACKEND_URL" --write-env   # first time only
go run . publish
```

Name the deployment on that first login. With no argument and no deployment yet
recorded on the active profile, `blocks login` asks which one to target, and
answering "Blocks Network" deliberately bypasses `BLOCKS_BACKEND_URL` — so a
local backend has to be named, either as the argument above or by an earlier
login that created a profile for it. A plain-`http` URL is accepted here because
the host is loopback; see
[What `blocks login` accepts as a deployment](#what-blocks-login-accepts-as-a-deployment).

Or use the Makefile (reads `.env` automatically):

```sh
make run ARGS=publish  # go run with ldflags, pass subcommand via ARGS
make build            # outputs ./blocks
make clean            # removes it
```

### From the repo root

You can also build the CLI from the blocks-sdk root via Make:

```sh
make -C cli build     # produces cli/blocks
make -C cli install   # installs to ~/.blocks/bin
```

### Build-time configuration

Some defaults are baked into the binary at build time rather than read from the
environment at run time. The Makefile and the release build pass them as
ldflags:

- `BLOCKS_INSTANCE_DOMAIN` — the DNS suffix short names expand to, so
  `blocks login acme` targets `https://acme.<domain>`. Defaults to `blocks.ai`.
  A build carrying a value that is not a DNS suffix (including an empty one)
  refuses the expansion with an error naming this variable and the value it was
  built with, rather than concatenating it into a URL; every other form of login,
  and the rest of the CLI, keeps working in such a build. Customers on an agreed
  custom domain pass a full URL or host to `blocks login` instead, which bypasses
  the expansion.

```sh
BLOCKS_INSTANCE_DOMAIN=blocks.example.test make build
```
