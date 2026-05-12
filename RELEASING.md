# Releasing

All releases are triggered by pushing a git tag. CI workflows detect the
tag, build the package, stamp the version from the tag name, and publish
to the appropriate registry. There are no version numbers in source
files to maintain -- the tag is the single source of truth.

## Tag Format

Each SDK has two tag prefixes -- one for internal Artifactory and one
for the public registry:

| SDK    | Artifactory (internal) | Public registry       |
| ------ | ---------------------- | --------------------- |
| Node   | `node-v*`              | `node-npm-v*`         |
| Python | `python-v*`            | `python-pypi-v*`      |
| CLI    | `cli-v*`               | `cli-npm-v*`          |

Append `-rc` to the version for a pre-release (e.g. `node-v0.2.0-rc`).
Pre-releases use the exact same pipeline -- npm and PyPI treat the `-rc`
suffix as a pre-release version that consumers won't get by default.

## Publishing

### Using Make (from the repo root)

```bash
# Artifactory (internal)
make release-node   VERSION=0.2.0
make release-python VERSION=0.2.0
make release-cli    VERSION=0.2.0

# Public registries
make publish-node   VERSION=0.2.0
make publish-python VERSION=0.2.0
make publish-cli    VERSION=0.2.0

# Pre-release
make release-node VERSION=0.2.0-rc
```

### Using git directly

```bash
# Artifactory (internal)
git tag node-v0.2.0   && git push origin node-v0.2.0
git tag python-v0.2.0 && git push origin python-v0.2.0
git tag cli-v0.2.0    && git push origin cli-v0.2.0

# Public registries
git tag node-npm-v0.2.0   && git push origin node-npm-v0.2.0
git tag python-pypi-v0.2.0 && git push origin python-pypi-v0.2.0
git tag cli-npm-v0.2.0    && git push origin cli-npm-v0.2.0
```

## What happens when you push a tag

### Node SDK (`node-v*` / `node-npm-v*`)

1. Checks out the repo and installs dependencies
2. Stamps `package.json` version via `npm version <ver> --no-git-tag-version`
3. Builds the TypeScript source
4. Publishes `@blocks-network/sdk` to the target registry

### Python SDK (`python-v*` / `python-pypi-v*`)

1. Checks out the repo
2. Stamps `pyproject.toml` version via sed
3. Builds the package with `python -m build`
4. Uploads to the target registry via twine

### CLI (`cli-v*`)

1. Checks out the repo and cross-compiles Go binaries for all supported
   platforms:
   - darwin/arm64, darwin/amd64
   - linux/arm64, linux/amd64
   - windows/amd64
   - freebsd/amd64, freebsd/arm64
   - openbsd/amd64, openbsd/arm64
2. Creates a GitHub Release with the archives, checksums, and install
   scripts (every platform above is included as a tarball/zip)
3. Packages the npm-published subset of binaries into platform packages
   (`@blocks-network/cli-darwin-arm64`, `cli-darwin-x64`, `cli-linux-arm64`,
   `cli-linux-x64`, `cli-win32-x64` — five total). FreeBSD and OpenBSD
   binaries are intentionally NOT npm-published; BSD users install via
   `install.sh`, which pulls from the GitHub Release archives.
4. Publishes the platform packages and the wrapper
   (`@blocks-network/cli`) to Artifactory

### CLI public npm (`cli-npm-v*`)

Cross-compiles and publishes only the five npm-supported targets
(darwin/arm64, darwin/amd64, linux/arm64, linux/amd64, windows/amd64)
to npmjs.org. No GitHub Release is created and no BSD binaries are
produced on this path — BSD users install via the `cli-v*` Release
archives, not via npm.

## Version safety

- Git tags are unique -- if a version has already been released, `git tag`
  will fail and nothing gets pushed.
- npm and PyPI also reject duplicate versions, so even if a tag somehow
  gets through, the publish step will fail safely.
- The `make` targets warn if you are not on the master branch.

## Checking existing versions

```bash
# List all Node releases
git tag -l 'node-v*' | sort -V

# List all Python releases
git tag -l 'python-v*' | sort -V

# List all CLI releases
git tag -l 'cli-v*' | sort -V

# Find the latest Node release
git tag -l 'node-v*' | sort -V | tail -1
```

## Rollback

If a bad version is published:

1. **npm** — `npm unpublish @blocks-network/<pkg>@<version>` (within 72 h)
   or publish a newer patch version with the fix.
2. **PyPI** — `pip install blocks-network==<previous-version>` is the
   recommended guidance. PyPI does not support true unpublish; yank the
   release on pypi.org and publish a fixed version.
3. **GitHub Release** — delete the release and tag from the GitHub UI,
   then re-tag and push once the fix is ready.

In all cases, publish a new patch version as the primary remediation.

## Secrets required

| Secret                 | Used by            |
| ---------------------- | ------------------ |
| `ARTIFACTORY_USERNAME` | All Artifactory    |
| `ARTIFACTORY_TOKEN`    | All Artifactory    |
| `NPM_TOKEN`            | Node + CLI public  |
| `PYPI_TOKEN`           | Python public      |
| `BLOCKS_BACKEND_URL`   | CLI builds         |
| `BLOCKS_CLI_CLIENT_ID` | CLI builds         |
