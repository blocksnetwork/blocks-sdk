# Test the Agent Plugin locally

The `blocks-sdk` repository follows Agent Plugins 1.0.0: `plugin.json` is at the
repository root and the two bundled Agent Skills are immediate children of
`skills/`. The skills also follow the layout discovered by `npx skills add`.
The steps below stage only the portable plugin files; SDK, CLI, and example
source files at the same repository root are not needed by a plugin client.
Each skill is also independently installable; neither skill relies on relative
paths into its sibling skill.

The commands work from either the standalone `blocks-sdk` repository or the
`blocks-sdk/` subtree in the `blocksnetwork` repository:

```bash
REPO_ROOT="$(git rev-parse --show-toplevel)"
if test -f "$REPO_ROOT/plugin.json"; then
  PLUGIN_ROOT="$REPO_ROOT"
else
  PLUGIN_ROOT="$REPO_ROOT/blocks-sdk"
fi
```

## Cursor

Cursor can load an Agent Plugin directly from `~/.cursor/plugins/local` during
development. A marketplace is not required for this local test.

1. Stage the package in Cursor's local plugin directory:

   ```bash
   CURSOR_PLUGIN="$HOME/.cursor/plugins/local/blocks-network"
   test ! -e "$CURSOR_PLUGIN" || {
     echo "$CURSOR_PLUGIN already exists; remove your previous test copy first"
     exit 1
   }

   mkdir -p "$CURSOR_PLUGIN/skills"
   cp "$PLUGIN_ROOT/plugin.json" "$CURSOR_PLUGIN/plugin.json"
   cp -R "$PLUGIN_ROOT/skills/blocks-getstarted" "$CURSOR_PLUGIN/skills/"
   cp -R "$PLUGIN_ROOT/skills/blocks-network" "$CURSOR_PLUGIN/skills/"
   ```

2. Restart Cursor or run **Developer: Reload Window**.

3. Open **Customize** and confirm these skills appear:
   - `blocks-getstarted`
   - `blocks-network`

4. In a new Agent chat, invoke a skill manually:

   ```text
   /blocks-network What command validates an agent card before publishing?
   ```

   Expected answer:

   ```text
   blocks check
   ```

5. Remove the local test copy when finished:

   ```bash
   rm -rf "$CURSOR_PLUGIN"
   ```

### Cursor troubleshooting

- If the plugin does not appear, confirm
  `~/.cursor/plugins/local/blocks-network/plugin.json` exists, then reload the
  window again.
- Copy the package again after changing its files; Cursor loads the staged copy,
  not the repository directory.

## Codex

Codex supports the portable root `plugin.json`, but it does not scan a
repository-local `.codex/plugins` directory. For a local test, create a small
local marketplace and install the staged package through it.

1. Create a temporary marketplace:

```bash
CODEX_MARKETPLACE="$(mktemp -d "${TMPDIR:-/tmp}/blocks-plugin-marketplace.XXXXXX")"
CODEX_PLUGIN="$CODEX_MARKETPLACE/plugins/blocks-network"
mkdir -p "$CODEX_MARKETPLACE/.agents/plugins" \
  "$CODEX_PLUGIN/skills"
cp "$PLUGIN_ROOT/plugin.json" "$CODEX_PLUGIN/plugin.json"
cp -R "$PLUGIN_ROOT/skills/blocks-getstarted" "$CODEX_PLUGIN/skills/"
cp -R "$PLUGIN_ROOT/skills/blocks-network" "$CODEX_PLUGIN/skills/"

cat > "$CODEX_MARKETPLACE/.agents/plugins/marketplace.json" <<'JSON'
{
  "name": "blocks-portable-local",
  "interface": {
    "displayName": "Blocks Portable Local"
  },
  "plugins": [
    {
      "name": "blocks-network",
      "source": {
        "source": "local",
        "path": "./plugins/blocks-network"
      },
      "policy": {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL"
      },
      "category": "Developer Tools"
    }
  ]
}
JSON
```

2. Register the marketplace and install the plugin:

```bash
codex plugin marketplace add "$CODEX_MARKETPLACE"
codex plugin add blocks-network@blocks-portable-local
```

3. Confirm that Codex reports the plugin as installed and enabled:

   ```bash
   codex plugin list --json
   ```

4. Start a new Codex session, or run a non-interactive smoke test:

   ```bash
   codex exec --ephemeral --skip-git-repo-check \
     --sandbox read-only -C /tmp \
     'Use the blocks-network skill. Answer only with the command that validates an agent card before publishing.'
   ```

   Expected answer:

   ```text
   blocks check
   ```

5. Remove the temporary installation when finished:

   ```bash
   codex plugin remove blocks-network@blocks-portable-local
   codex plugin marketplace remove blocks-portable-local
   rm -rf "$CODEX_MARKETPLACE"
   ```

### Codex troubleshooting

- Start a new Codex session after installation; an already-running session does
  not gain newly installed skills.
- Install and launch Codex with the same `CODEX_HOME`. If one shell reports the
  plugin but another does not, compare:

  ```bash
  printf '%s\n' "${CODEX_HOME:-$HOME/.codex}"
  ```

- The marketplace in this procedure lives in a temporary directory. Keep that
  directory until cleanup; updates or reinstalls need the marketplace source.

## Scope of this test

These checks prove that Cursor and Codex can discover and invoke the two skills
from the portable Agent Plugin. They do not test marketplace submission, review,
remote installation, or update delivery.
