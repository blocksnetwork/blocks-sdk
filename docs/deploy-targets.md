# Pluggable Deploy Targets

`blocks deploy <target>` resolves the target from a registry that the
CLI populates at startup. Three targets are built in: `cloudflare`,
`vercel`, and `netlify`. Anyone can add a new target without
recompiling the CLI by dropping a YAML config file into
`~/.config/blocks/deploy-targets/<name>.yml`.

## How resolution works

1. The CLI registers the three built-in adapters at startup.
2. Then it scans
   `$XDG_CONFIG_HOME/blocks/deploy-targets` (falling back to
   `~/.config/blocks/deploy-targets`) for `*.yml` and `*.yaml` files.
3. For each file, the CLI registers an adapter. **Disk adapters
   override built-ins of the same name** — so you can override the
   `cloudflare` behavior locally by writing your own
   `cloudflare.yml`.
4. `blocks deploy --list` prints the registered set, marked
   `[builtin]` or `[disk]`.

## Plugin YAML shape

```yaml
# Optional, defaults to 1 when omitted. The CLI rejects configs with
# an unknown protocolVersion. Reserved for future bumps.
protocolVersion: 1

# Identity
name: railway              # used as `blocks deploy railway`
description: "Railway static deploy"

# Executable. Either a single string ("/usr/local/bin/blocks-railway")
# or a list (["sh", "-c", "node /path/to/script.js"]).
command: "/usr/local/bin/blocks-railway"

# Optional env vars injected into the subprocess. The following
# placeholders are expanded at exec time:
#   $ASSETS_DIR              absolute path to the web/ folder
#   $PROJECT_NAME            the project directory name
#   $BLOCKS_DEPLOY_TARGET    this target's name
env:
  BLOCKS_DEPLOY_DIR: "$ASSETS_DIR"
  BLOCKS_PROJECT_NAME: "$PROJECT_NAME"

# Credentials
# credentialFlow: "api-token" | "browser-grant" | "none"
credentialFlow: api-token
credentialPrompt: "Paste your Railway API token: "
credentialEnvVar: RAILWAY_API_TOKEN
```

When `credentialFlow` is `api-token`, the CLI:

1. Reads `$RAILWAY_API_TOKEN` from the parent shell env if set.
2. Otherwise prompts the user with `credentialPrompt`.
3. Injects the token into the subprocess as `$RAILWAY_API_TOKEN`.

Stored-token persistence for plugin targets is not yet implemented;
either export the env var in your shell or paste the token each run.

## I/O contract

When the CLI invokes a plugin:

- **cwd**: the project root (where `blocks.config.json` sits).
- **env**: parent env + the configured `env` map (with placeholder
  expansion) + the credential env var.
- **stdin**: closed.
- **stdout**: streamed; the **last non-empty line** is the deployed
  URL.
- **stderr**: streamed verbatim to the CLI's stderr (progress,
  errors).
- **exit code**: zero is success. Non-zero surfaces the captured
  stderr as the failure message and does NOT persist
  `lastDeployedUrl`.

## Minimal worked example

`~/.config/blocks/deploy-targets/echo.yml`:

```yaml
name: echo
description: "Echo URL plugin (smoke test)"
command: ["/bin/echo", "https://example.com"]
credentialFlow: none
```

Then:

```bash
blocks deploy --list
#   cloudflare    [builtin]  Cloudflare Pages (direct upload)
#   echo          [disk]     Echo URL plugin (smoke test)
#   netlify       [builtin]  Netlify (zip deploy)
#   vercel        [builtin]  Vercel (REST file upload + deployment)

blocks deploy echo
# Deploying web/ to echo...
# Deployed: https://example.com
```

## A realistic shell-script plugin

A plugin that wraps `rsync` to a static-hosting box:

```yaml
name: my-staging
description: "rsync to staging.internal:/srv/blocks"
command: ["/usr/local/bin/blocks-rsync-deploy"]
credentialFlow: none
env:
  RSYNC_SRC: "$ASSETS_DIR"
  RSYNC_DEST: "staging.internal:/srv/blocks/$PROJECT_NAME"
```

`/usr/local/bin/blocks-rsync-deploy`:

```bash
#!/bin/sh
set -e
rsync -a --delete "$RSYNC_SRC/" "$RSYNC_DEST/"
echo "https://staging.internal/$BLOCKS_PROJECT_NAME"
```

## Version handling

`protocolVersion` is optional; omitting it implies `1` (the only
version v0 understands). When the CLI adds a v2 protocol with a
different I/O contract, v1 plugins will continue to load until they
declare a higher version explicitly. Plugins authored against a
future version that this CLI doesn't recognize are rejected at load
time with a clear error.
