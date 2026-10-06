"use strict";

// Parity with package.json and cli-npm-publish.yaml: test/platform.test.js
const PLATFORM_PACKAGES = {
  "darwin arm64": { pkg: "@blocks-network/cli-darwin-arm64", binary: "blocks" },
  "darwin x64": { pkg: "@blocks-network/cli-darwin-x64", binary: "blocks" },
  "freebsd arm64": { pkg: "@blocks-network/cli-freebsd-arm64", binary: "blocks" },
  "freebsd x64": { pkg: "@blocks-network/cli-freebsd-x64", binary: "blocks" },
  "linux arm64": { pkg: "@blocks-network/cli-linux-arm64", binary: "blocks" },
  "linux x64": { pkg: "@blocks-network/cli-linux-x64", binary: "blocks" },
  "openbsd arm64": { pkg: "@blocks-network/cli-openbsd-arm64", binary: "blocks" },
  "openbsd x64": { pkg: "@blocks-network/cli-openbsd-x64", binary: "blocks" },
  "win32 x64": { pkg: "@blocks-network/cli-win32-x64", binary: "blocks.exe" },
};

function unsupportedPlatformMessage(key) {
  return [
    `The Blocks CLI npm package does not support ${key}.`,
    `Supported platforms: ${Object.keys(PLATFORM_PACKAGES).join(", ")}.`,
    "No Blocks CLI build is published for this platform.",
  ].join("\n");
}

function missingPackageMessage(key, pkg) {
  return (
    `The Blocks CLI binary package for ${key} (${pkg}) is not installed.\n` +
    `npm skips it when optional dependencies are omitted. Reinstall with:\n` +
    `  npm install -g @blocks-network/cli --include=optional`
  );
}

function resolveBinary() {
  const key = `${process.platform} ${process.arch}`;
  const entry = PLATFORM_PACKAGES[key];
  if (!entry) {
    throw new Error(unsupportedPlatformMessage(key));
  }
  try {
    return require.resolve(`${entry.pkg}/${entry.binary}`);
  } catch {
    throw new Error(missingPackageMessage(key, entry.pkg));
  }
}

module.exports = { PLATFORM_PACKAGES, resolveBinary };
