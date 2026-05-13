#!/usr/bin/env node

const fs = require("fs");
const path = require("path");
const os = require("os");
const https = require("https");

const PLATFORMS = {
  "darwin arm64":  "@blocks-network/cli-darwin-arm64/blocks",
  "darwin x64":    "@blocks-network/cli-darwin-x64/blocks",
  "freebsd arm64": "@blocks-network/cli-freebsd-arm64/blocks",
  "freebsd x64":   "@blocks-network/cli-freebsd-x64/blocks",
  "linux arm64":   "@blocks-network/cli-linux-arm64/blocks",
  "linux x64":     "@blocks-network/cli-linux-x64/blocks",
  "win32 x64":     "@blocks-network/cli-win32-x64/blocks.exe",
};

const INSTALL_DIR = path.join(os.homedir(), ".blocks", "bin");
const BINARY_NAME = process.platform === "win32" ? "blocks.exe" : "blocks";

function fetchAndCacheConfig() {
  const MAX_BODY = 1024 * 1024; // 1 MB
  return new Promise((resolve) => {
    const configDir = path.join(os.homedir(), ".blocks");
    const configPath = path.join(configDir, "config.json");

    const req = https.get("https://config.blocks.ai/config.json", (res) => {
      if (res.statusCode !== 200) {
        res.resume();
        resolve();
        return;
      }
      let data = "";
      let bytes = 0;
      res.on("data", (chunk) => {
        bytes += chunk.length;
        if (bytes > MAX_BODY) { res.destroy(); resolve(); return; }
        data += chunk;
      });
      res.on("end", () => {
        try {
          const parsed = JSON.parse(data);
          if (parsed.api && parsed.api.baseUrl) {
            fs.mkdirSync(configDir, { recursive: true, mode: 0o700 });
            const tmpPath = configPath + ".tmp";
            fs.writeFileSync(tmpPath, JSON.stringify(parsed, null, 2), { mode: 0o600 });
            fs.renameSync(tmpPath, configPath);
            console.log(`Cached config -> ${configPath}`);
          }
        } catch { /* non-fatal */ }
        resolve();
      });
    });

    req.on("error", () => resolve());
    req.setTimeout(5000, () => { req.destroy(); resolve(); });
  });
}

async function main() {
  const key = `${os.platform()} ${os.arch()}`;
  const pkg = PLATFORMS[key];
  if (!pkg) {
    console.error(`Unsupported platform: ${key}`);
    process.exit(1);
  }

  let srcBinary;
  try {
    srcBinary = require.resolve(pkg);
  } catch {
    console.error(
      `Platform package not found: ${pkg.split("/")[0]}\n` +
      `Try: npm i ${pkg.split("/")[0]}`
    );
    process.exit(1);
  }

  // Copy binary to ~/.blocks/bin/
  fs.mkdirSync(INSTALL_DIR, { recursive: true });
  const dest = path.join(INSTALL_DIR, BINARY_NAME);
  fs.copyFileSync(srcBinary, dest);
  fs.chmodSync(dest, 0o755);
  console.log(`Installed blocks -> ${dest}`);

  // Cache remote config locally (avoids TLS issues on Windows at runtime)
  await fetchAndCacheConfig().catch((err) => {
    console.warn(`Warning: failed to pre-cache config: ${err.message || err}`);
  });

  // Add ~/.blocks/bin to PATH
  if (process.platform !== "win32") {
    addToPath();
  } else {
    addToPathWindows();
  }
}

function addToPath() {
  const shellName = path.basename(process.env.SHELL || "/bin/sh");
  let profile;
  switch (shellName) {
    case "zsh":  profile = path.join(os.homedir(), ".zshrc"); break;
    case "bash": profile = path.join(os.homedir(), ".bashrc"); break;
    default:     profile = path.join(os.homedir(), ".profile"); break;
  }

  const pathLine = 'export PATH="$HOME/.blocks/bin:$PATH"';

  try {
    const contents = fs.readFileSync(profile, "utf8");
    if (contents.includes(".blocks/bin")) {
      console.log(`${INSTALL_DIR} already in PATH (${profile})`);
      return;
    }
  } catch {
    // Profile doesn't exist yet, we'll create it
  }

  fs.appendFileSync(profile, `\n# Blocks CLI\n${pathLine}\n`);
  console.log(`Added ${INSTALL_DIR} to PATH in ${profile}`);
  console.log(`Run 'source ${profile}' or open a new terminal to use 'blocks'.`);
}

function addToPathWindows() {
  console.log(
    `Add ${INSTALL_DIR} to your PATH environment variable,\n` +
    `or run: setx PATH "%PATH%;${INSTALL_DIR}"`
  );
}

main();
