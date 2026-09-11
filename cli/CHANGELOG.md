# Changelog

All notable changes to the Blocks CLI are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Older entries live in [../CHANGELOG.md](../CHANGELOG.md) pending backfill.

## [Unreleased]

### Added

- `blocks unregister` removes an agent from the deployment you are currently
  targeting, the inverse of `blocks register`. Takes the name from
  `agent-card.json` in the current directory, or as an argument. Prompts for
  confirmation. In non-interactive use (CI, scripts) it requires `--yes`, and
  refuses with an error otherwise.
- Short-name login — `blocks login acme` targets `https://acme.blocks.ai`. A
  name matching an existing profile uses that profile's deployment instead, so
  custom domains keep working.
- A deployment prompt on first login — `blocks login` with no stored deployment
  now asks which one to target instead of assuming. It is asked once: any
  completed login settles the question, including a login to Blocks Network,
  which stores no deployment URL of its own. Non-interactive and scripted use
  are unchanged, and `--no-input` turns the prompt into an error naming the flags
  that answer it rather than choosing for you.
- Deployment context before state-changing commands — `register`, `publish`,
  `unregister`, `invite send`, `invite revoke`, and `invite accept` print the
  deployment they are about to act on. `register` and `publish` also name the
  organization the agent will be created under, because that is what scopes
  them. `unregister` and `invite accept` name the deployment alone, and
  `invite send` / `invite revoke` name the agent and the grantee, because those
  operations are authorized by your rights over the named agent rather than by
  the organization your key belongs to. Where an organization is named and
  cannot be established locally — a key belonging to another deployment, or one
  no saved profile has ever seen — it is reported as unknown rather than
  guessed. A key you supplied yourself is not automatically unknown: when it
  matches one already cached for that deployment, the organization is named.
- `--mode connect-agent` and `--mode call-agent` — accepted as alternative
  spellings of `--mode provider` and `--mode consumer`. Existing values keep
  working unchanged.
- `blocks login --network` — targets Blocks Network explicitly without prompting.
  Useful for scripted use where the deployment choice must be non-interactive.
- Global `--no-input` flag — asks commands not to read stdin. Where it is
  honoured, a prompt that would otherwise be required becomes an error naming the
  flag — or, for a hosting partner's API token, the environment variable — that
  answers it, instead of a hang. Coverage is not universal, and the organization
  picker shown during a browser login has no answer flag at all, so the flag is
  not a guarantee that a command cannot block. Its own help text names the current
  exceptions and is kept in step with the CLI: run `blocks --help`.
- `blocks init` deployment header — an interactive `blocks init` run with no name
  argument and no `--mode` now shows which deployment you are targeting before
  offering the project-kind choice, and includes an Enterprise hint for fresh
  Network users. Runs that already name a project or a mode go straight to the
  wizard as before.

### Changed

- Error messages on Enterprise deployments now name the deployment you are
  working with and say what to do next. Where a message told you to "run
  `blocks login`" without saying where, it now names your profile and its URL
  when one exists, or tells you to pass your instance URL when it does not —
  including where an API key comes from (the deployment's dashboard). A
  missing backend URL now says to log in first or set `BLOCKS_BACKEND_URL`,
  instead of naming internal configuration. A permission failure names the
  permission and the organization it was refused in. `blocks register` no
  longer tells you to fix validation errors "before publishing", and a failed
  request names the deployment it was sent to. Messages on Blocks Network are
  unchanged.
- `blocks login` now refuses a plain-`http` deployment URL unless its host is
  your own machine. `https://` is required for every other host, and the
  authority has to be a host with an optional port in the range 1–65535, so a
  URL of the form `user@host`, or one with a port outside that range, is refused
  as well. A query string or a fragment is refused too: the argument is an origin
  the CLI appends endpoint paths to, so `https://host?x=1` would have sent every
  request to a path no deployment serves — pass the deployment's origin without
  either. If you were signing in to a deployment over `http://`, use its
  `https://` URL; `http://localhost`, `http://127.0.0.1` and `http://[::1]` keep
  working for local development. A custom port and a path prefix are still
  accepted. A refused argument names the accepted forms and nothing is sent.
- `--no-input` now fails instead of choosing for you on the two `blocks login`
  decisions that previously fell back to a silent default when stdin was not a
  terminal: which deployment to target, and whether to write the project `.env`.
  Each is now an error naming the flag that answers it, because asking not to be
  prompted is also a refusal to be guessed at — silently landing on Blocks
  Network would have signed you in somewhere you never named. Pass `--network`
  (or an instance URL / short name) and `--write-env` / `--no-write-env`; a key
  passed as `--api-key` or `--api-key-stdin` answers both on its own. **Without**
  `--no-input`, a non-interactive run still defaults silently exactly as before,
  so existing CI is unaffected.
- The deployment line printed before `unregister`, `invite send`,
  `invite revoke` and `invite accept` no longer names an organization. Those
  commands are authorized by your rights over the named agent or invitation, not
  by the organization your key belongs to, so naming one read as a promise about
  what could be affected that it did not make. `invite send` and `invite revoke`
  now name the agent and the grantee instead, and `unregister` and
  `invite accept` name the deployment alone — their own confirmation or success
  line already names the subject. `register` and `publish` still name the
  organization, because there it is what the operation is scoped to.
- A sign-in that follows a deployment URL exported in your shell now saves the
  key under a profile named for that deployment, creating and activating it if
  needed, rather than writing into whichever profile was previously active. This
  keeps a later `blocks profile use` from returning you to a profile holding
  another deployment's key. Existing profiles are not modified or removed; if you
  relied on the old behaviour to refresh a specific profile's key, name it with
  `blocks login --profile <name>`.
- `blocks logout` now names the deployment it keeps, and how to remove it.
  Behaviour is unchanged — your deployment target is preserved so a later
  `blocks login` returns to the same instance. This message appears only when
  you are on a named deployment, not on the default one.
- On deployments without a marketplace, billing and pricing lines no longer
  appear in `register` and `publish` output.
- Pricing limits are now requested only once a publish is known to be paid.
  Nothing else reads them, so a free publish makes no such request — that covers
  `--billing-mode free` on Blocks Network as well as deployments with no
  marketplace — and neither does an interactive publish that chooses free at the
  prompt, or one rejected for conflicting pricing flags. A paid publish still
  prompts against the deployment's own price range. Previously the request went
  out before any of that was known, which on a deployment that accepts the
  connection without answering cost a five-second wait for a value the publish
  could not use.
- `blocks register` now reports the agent as registered rather than published.
- The `--write-env` prompt now reads "Write credentials to project .env?"
  (it previously named only the API key).
- An invalid `--mode` value now reports which values are accepted, listing all
  of them.
- `blocks init` next-steps output now omits the login line when already logged in,
  and prints an instance-qualified login command on enterprise deployments to
  prevent accidentally authenticating against the wrong deployment. For fresh
  users, the login line now explicitly names Blocks Network and includes an
  Enterprise option to clarify the choice.
- **`blocks deploy` no longer reads a hosting partner's API token out of a project
  `.env`.** `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `VERCEL_TOKEN`,
  `VERCEL_TEAM_ID` and `NETLIFY_AUTH_TOKEN` are taken from the environment only.
  If you kept one in a project `.env`, export it in your shell instead
  (`export CLOUDFLARE_API_TOKEN=…`), or run `blocks login --provider <partner>`
  once to store a token — exporting was already the documented way to set these
  for a non-interactive deploy. The CLI prints a note naming the variable and the
  file when a `.env` supplies one, so a setup that relied on it says so rather
  than failing silently. Everything else in your `.env` still reaches your agent.
- On a deployment with no marketplace, `blocks publish --billing-mode paid` is now
  rejected with an error naming the value that works, instead of sending a paid
  listing to a deployment where billing is switched off. Publish free there —
  omit `--billing-mode`, or pass `--billing-mode free`. Blocks Network accepts
  both modes as before. Previously such a publish reported success and left an
  agent that was free forever with no pricing anywhere to change.

- Variable names beginning `BLOCKS_` are now matched without regard to letter
  case when the CLI edits a project `.env`. A `.env` holding both
  `blocks_api_key` and `BLOCKS_API_KEY` holds one variable on Windows, where the
  process environment ignores case, and the CLI previously compared the two names
  literally — so a write updated one spelling and left the other in place, and
  `blocks logout` or `blocks profile remove` deleted only the spelling it was
  asked about. A write now leaves a single assignment of the variable, under
  whichever spelling you used, and a removal clears every spelling of it. This
  applies on every platform, not only Windows, and that is the behaviour change to
  be aware of: on macOS and Linux a lowercase `blocks_...` line you added
  deliberately is now a line `blocks login` will overwrite and `blocks logout`
  will delete. Nothing reads those variables under a lowercase name, so a second
  spelling was already inert. Variables outside the `BLOCKS_` prefix are
  unaffected and are still matched exactly, because `FOO` and `foo` really are two
  variables on macOS and Linux and a project may keep both on purpose.

### Fixed

- A mistyped command's "Did you mean this?" suggestions are indented
  correctly again. The tab characters indenting each suggestion were being
  rendered as a literal `\x09`, so every typo produced a garbled hint instead
  of a readable one.
- A logged-out CLI no longer reports a credential-store failure when nothing
  is wrong with the store. The leftover credentials file from an earlier
  version, drained of its Blocks key by the move to profiles, was being read
  as a broken store — so logging out and then publishing or registering
  printed "failed to load credentials" instead of the sign-in guidance. An
  empty Blocks slot is now treated as simply not signed in; a store that
  genuinely cannot be read still reports the failure.
- `blocks profile list`, `blocks whoami`, and `blocks profile use` now report when
  `BLOCKS_BACKEND_URL` overrides the selected profile and decides where commands
  actually go. Previously all three described the saved profile as though it were
  the target, so a deployment URL set in your environment could point every command
  at a different deployment with nothing to show it. Nothing extra is printed when
  no override is in force, or when the override points at the selected profile's
  own deployment. `blocks whoami --json` gains a `backend_url_override` field,
  present only while an override is in force.
- `blocks profile remove` now also clears what the project `.env` still held for
  the deployment being removed: each of its two deployment URLs that names it —
  the two are judged independently — and the `BLOCKS_API_KEY` beside them when
  the backend URL was one of them, since the backend URL is what records where
  that key is spent, or when the key is the one only the removed profile had
  cached. A `.env` whose only match is the configuration endpoint therefore loses
  that line and keeps the key. It says which lines it removed.
  Previously all of them outlived the profile, so every later command in that
  directory kept sending a key to a deployment the profile list no longer
  mentioned. It leaves the `.env` completely alone when another profile still
  describes the same deployment — removing one of two names for one deployment,
  such as an alias and a host slug, forgets a name and not the deployment, and the
  surviving profile still targets it. Values naming any other deployment are left
  alone, and a `.env` that exists but cannot be read or rewritten now fails
  the command instead of being reported as nothing to remove — with the profile
  left in place, so re-running it retries both halves. `blocks logout`
  deliberately differs: it removes only the key and keeps the deployment URLs,
  because the profile survives and `blocks login` is meant to return to it.
- `blocks run` now works correctly in freshly scaffolded projects when logged in.
  Previously, the empty `BLOCKS_API_KEY=` placeholder in `.env` would prevent 
  credential injection, causing runs to fail with a credentials error despite 
  being logged in.
- `blocks login --write-env` now writes the deployment's URLs alongside the API
  key. Scripts run directly — a trigger or consumer outside `blocks run` —
  previously fell back to the default deployment regardless of where you logged
  in. The variables written are `BLOCKS_API_KEY`, `BLOCKS_BACKEND_URL` and
  `BLOCKS_CDM_URL`. Both URLs are needed: the first sets the API your agent
  calls, the second the configuration it starts from, and writing only one left
  a directly launched agent using the default deployment's configuration against
  your deployment's API. When you sign in to the default deployment neither is
  written, and any stale values are removed. All three are applied in a single
  edit, so an interrupted sign-in cannot leave a new key beside an old URL.
- `blocks login <host>` without a scheme (e.g. `blocks login acme.example.com`)
  previously failed; the host is now resolved correctly.
- `blocks init --yes` now skips the login offer prompt as documented. Previously
  it would still prompt "Log in now to access your private agents?" even when
  `--yes` was set.
- `blocks login` now respects explicit Network deployment choices. When choosing
  "Blocks Network" at the prompt or using `--network`, the CLI no longer
  incorrectly uses enterprise URLs from environment variables or profiles, and
  credentials are now stored under the correct profile instead of an active
  enterprise profile.
- Profile metadata corruption no longer persists after Network login. When
  logging into Blocks Network, stale enterprise branding is automatically
  cleared from the profile.
- Project environment files remain consistent after deployment switches.
  When switching from an enterprise deployment to the default one, the stale
  deployment URLs left in `.env` by the earlier sign-in are now removed, so a new
  key cannot sit beside another deployment's targeting.
- Deployment context banners now accurately reflect the active deployment and
  credentials. Commands show the deployment and organization the command will
  actually use, rather than saved profile information. When overrides make the
  organization unverifiable, this is indicated instead of displaying incorrect
  information.
- `blocks login <alias>` now updates the existing profile when the alias matches
  a known deployment. Previously, it would create duplicate profiles named after
  the resolved hostname.
- `blocks login --write-env` now writes the backend URL of the deployment it
  actually signed in to. When `BLOCKS_BACKEND_URL` pointed at one deployment while
  a saved profile named another, the API key and the backend URL written to `.env`
  could describe different deployments, and every later command sent that key to
  the wrong place. Both values now come from the same deployment.
- `blocks invite` now uses the API key from `BLOCKS_API_KEY` when one is set,
  matching every other command and the deployment context line printed before the
  action. Previously it silently used the saved profile's key instead, so the line
  you were shown did not describe the request that was sent.
- `--no-input` now works for `blocks publish` and `blocks register`. Previously
  these commands could still stop and ask about visibility, pricing, or your
  organization name when run in a terminal, so a script that had explicitly asked
  not to be prompted could hang. They now fail immediately with an error naming
  the flag to pass. The flag's help text now names the prompts that are still not
  covered, instead of implying full coverage, and the README points at that help
  text rather than repeating a list that can go stale.
- Deployment terminology and billing behaviour now follow the deployment being
  called. With an Enterprise profile saved but `BLOCKS_BACKEND_URL` pointing at a
  Blocks Network deployment, `publish` and `register` treated the target as
  Enterprise: billing was forced to free and the marketplace prompts were skipped
  against a deployment that has both.
- The deployment a command targets, the credential it sends, and the deployment
  context line it prints are now resolved once per command. Combinations of
  profiles, `BLOCKS_BACKEND_URL`, `BLOCKS_API_KEY`, `--api-key` and
  `--api-key-stdin` can no longer produce a command that acts on one deployment
  while reporting another.
- Success and confirmation messages now name the deployment the command is
  actually using. With a saved Enterprise profile and `BLOCKS_BACKEND_URL`
  pointing somewhere else, `init`, `login`, `publish`, `register` and
  `unregister` named the profile's product while acting on the other deployment
  — including the confirmation prompt that asks you to approve removing an
  agent. Where the product name cannot be confirmed for the deployment being
  called, these messages now name that deployment instead. `blocks logout` still
  names your saved deployment, because that is what it clears.
- Choosing "Enterprise instance" at the deployment prompt and then pressing
  Enter without typing anything no longer signs you in to Blocks Network.
  `blocks login` now asks again, and stops with an error naming what it needs if
  the answer is still empty.
- `--no-input` now works for `blocks init`. Previously it still asked for the
  agent name, and asked for confirmation before scaffolding, so a script that had
  asked not to be prompted could hang. It now fails immediately with an error
  naming the argument or flag to pass. `blocks init --mode webapp` likewise
  reports the missing `--agent` instead of starting its wizard.
- On a multi-organization deployment, the deployment context line printed before
  `publish` and `register` now names the organization the request is actually
  made as. Previously it printed before you chose an organization, so it could
  name one organization while the agent was created under another. The
  organization picker now states the deployment before it asks, so nothing acts
  before you have been told where. An organization you selected is also no longer
  replaced when you supplied a key explicitly, and your default organization is
  re-pointed only once the agent has actually been accepted — a run you cancel at
  a later prompt no longer changes what later commands authenticate as.
- `blocks run` now starts your agent against the deployment the command is
  targeting. When a deployment URL in your environment pointed somewhere other
  than your saved profile, the agent fell back to the default deployment's
  configuration — so it could register against one deployment while connecting
  with another's settings.
- A saved deployment's API key is no longer sent to a different deployment. With
  a profile saved for one deployment and `BLOCKS_BACKEND_URL` pointing at
  another, commands sent the saved key to the deployment named by the
  environment variable, which usually failed with a confusing authentication
  error. Commands now stop with an error naming the deployment being called and
  how to supply a key for it. Setting `BLOCKS_BACKEND_URL` together with
  `BLOCKS_API_KEY`, or passing `--api-key`, works exactly as before, and so does
  the ordinary case where your profile and the deployment being called are the
  same.
- `--no-input` now covers every prompt under `blocks deploy` — the target picker,
  the hosting partner's token prompt, a user-defined target's credential prompt,
  and the post-deploy agent-card update — so none of them reads stdin. Previously
  these could still stop and read stdin, so a deploy that had asked not to be
  prompted could hang. Each is closed in the way that fits it. A token prompt is
  an actionable error naming the partner's environment variable (or
  `blocks login --provider <partner>` run once beforehand). A missing target is
  not an error: it falls back to the positional target or the saved
  `deployTarget`, and only errors when you have set neither. The post-deploy
  agent-card update is skipped: your card is left unchanged, a note names the
  agent and `--no-card-update`, and the deploy still succeeds — that question
  comes after your site is live, so it can never decide whether the command
  failed. Pass `--no-card-update` to state that intent and silence the note. One
  deploy question has no answer and is a hard failure under `--no-input` with no
  override flag: if your `web/` bundle was built for a different backend than the
  one you are now targeting, re-run
  `blocks init --mode webapp --agent <agent> --backend-url <backend>`, or target
  the backend the bundle was built for. Without `--no-input`, a non-interactive
  deploy still warns and continues exactly as before.
- A `blocks deploy` that uploaded your site no longer exits with a failure code
  because something after the upload could not be finished. Adding the deployed
  URL to a local agent card, and everything that can go wrong while doing it — an
  unreadable or unparseable card, a card already holding the maximum 25 web-app
  entries, a project directory name too long to use as a label — is now reported
  as a warning and skipped. Previously some of those ended the command with a
  non-zero exit code even though the deployment was live, so a pipeline that
  retries on failure would deploy a second time. A malformed
  `--card-path <agent>=<path>` is now rejected up front instead, before anything
  is uploaded.
- A bare `blocks login` in a directory whose `.env` pins a deployment you have a
  profile for now signs in to that deployment without asking. Previously it asked
  again whenever the pinned deployment was not the *active* profile's, and pressing
  Enter accepted the Blocks Network default — signing you in somewhere the
  directory never named, and caching the key in a profile no later command there
  resolves. This keeps the promise `blocks logout` prints when it preserves your
  deployment target. A genuine first sign-in, with nothing recording or naming a
  deployment, is still asked.
- `blocks profile remove` now re-reads the profile list immediately before it
  rewrites the project `.env`, instead of deciding from a snapshot taken earlier
  in the command. A `blocks login` that added a second name for the same
  deployment in between no longer loses that deployment's URLs and key from
  `.env`. Two removals running at the same moment can still interleave — the
  window is narrower, not gone.

### Security

- A value in a project `.env` can no longer point a command, or send your credentials,
  at a deployment you have not signed in to. Values you export in your own shell stay
  authoritative, so scripted and CI use is unaffected, and a `.env` that
  `blocks login --write-env` wrote for its own deployment keeps working. When a value is
  declined the CLI names it and says how to keep it.
- `blocks login <name>` refuses an argument that is not a valid host or short name, and
  expands a short name only when the result is a usable hostname.
- An explicitly requested Blocks Network sign-in reaches Blocks Network even when a
  configuration endpoint is set in your environment. The CLI says on stderr when it does
  not use such a value, and leaves your environment as you set it.
- Text the CLI did not write — deployment, organization and agent names, addresses, and
  messages a deployment returns — can no longer rewrite or reorder the lines it shares
  with a decision you are asked to make, such as the deployment shown before a
  state-changing command or the confirmation for a destructive one. Right-to-left names
  carry their own direction and are unaffected.
- A line the CLI offers you to copy names the command and leaves the value to you, so a
  name or URL from a checked-in file or from a deployment's own response cannot add
  shell words to something you paste into a terminal.
- A project `.env` configures your agent, and can retarget the CLI only through a fixed
  list of seven settings. Nothing else in the file reaches the CLI's own process, so
  nothing a cloned repository ships can change the transport the CLI uses, which
  certificates it trusts, where it keeps your credentials, or what it executes. Nothing
  is lost from your agent — `blocks run` still hands it the whole file, so application
  settings, `PYTHONPATH`, `NODE_OPTIONS` and the rest keep arriving as before. For a few
  names people commonly put in a `.env` and would be surprised to see ignored, the CLI
  names the variable and the file and asks you to export it in your shell instead.
- A credential or deployment URL that could not survive being written to and read back
  from a project `.env` is refused rather than stored: the CLI names the variable it
  refused and writes nothing, so there is no partly written file to mistake for a
  working one.
- A credential written to a project `.env` is readable only by your own account, whatever
  permissions the file had beforehand. A `.env` you deliberately kept group- or
  world-readable is narrowed to owner-only; one `chmod` widens it again if you need that.
- Where a project `.env` is a symlink, the write follows the link only when it points at
  an environment file of the same name inside the same project. Anything else is refused,
  with both ends named and nothing written. To keep such a setup, rename the link to
  match the file it points at, or run the command in the directory holding the real file.
  A shared `.env` one level up keeps working unchanged.
- `blocks logout` and `blocks profile remove` no longer report a credential removed from
  a project `.env` while a working one survives in the same file.
