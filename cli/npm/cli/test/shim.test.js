"use strict";

const assert = require("node:assert/strict");
const { spawn, spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { PLATFORM_PACKAGES } = require("../lib/platform");

const WRAPPER_DIR = path.join(__dirname, "..");
const SHIM = path.join(WRAPPER_DIR, "bin", "blocks");
const HOST = PLATFORM_PACKAGES[`${process.platform} ${process.arch}`];
const INTERRUPTED_EXIT = 130;

function runShim(nodeArgs = []) {
  return spawnSync(process.execPath, [...nodeArgs, SHIM, "--version"], {
    encoding: "utf8",
    env: { ...process.env, NODE_PATH: "" },
  });
}

const unsupportedPlatforms = [
  { platform: "aix", arch: "ppc64" },
  { platform: "win32", arch: "arm64" },
];

for (const { platform, arch } of unsupportedPlatforms) {
  test(`unsupported ${platform} ${arch} fails with the supported list`, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "blocks-shim-"));
    t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
    const preload = path.join(dir, "platform.js");
    fs.writeFileSync(
      preload,
      `Object.defineProperty(process, "platform", { value: "${platform}" });\n` +
        `Object.defineProperty(process, "arch", { value: "${arch}" });\n`,
    );

    const result = runShim(["--require", preload]);

    assert.equal(result.status, 1);
    assert.match(result.stderr, new RegExp(`does not support ${platform} ${arch}`));
    assert.match(result.stderr, /Supported platforms: darwin arm64, /);
    assert.match(result.stderr, /No Blocks CLI build is published for this platform/);
  });
}

// No platform package sits next to the source tree, the same state as after `--omit=optional`.
test("missing platform package fails with the reinstall command", () => {
  const result = runShim();

  assert.equal(result.status, 1);
  assert.match(result.stderr, /is not installed/);
  assert.match(result.stderr, /npm install -g @blocks-network\/cli --include=optional/);
});

// The shim's own copy sits beside a fake platform package, laid out as npm installs it.
function installShimWithFakeBinary(dir, script) {
  fs.cpSync(path.join(WRAPPER_DIR, "bin"), path.join(dir, "bin"), { recursive: true });
  fs.cpSync(path.join(WRAPPER_DIR, "lib"), path.join(dir, "lib"), { recursive: true });
  const pkgDir = path.join(dir, "node_modules", HOST.pkg);
  fs.mkdirSync(pkgDir, { recursive: true });
  fs.writeFileSync(path.join(pkgDir, HOST.binary), script, { mode: 0o755 });
  return path.join(dir, "bin", "blocks");
}

const POSIX_HOST = process.platform !== "win32" && HOST;
const HAS_EXECVE = typeof process.execve === "function";

function makeTempDir(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "blocks-shim-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

test(
  "the binary takes over the shim's process, so signals to blocks reach it directly",
  { skip: !(POSIX_HOST && HAS_EXECVE) && "needs POSIX and process.execve" },
  (t) => {
    const shim = installShimWithFakeBinary(makeTempDir(t), "#!/bin/sh\necho $$\n");

    const result = spawnSync(process.execPath, [shim], { encoding: "utf8", env: { ...process.env, NODE_PATH: "" } });

    assert.equal(result.status, 0, result.stderr);
    assert.equal(Number(result.stdout.trim()), result.pid);
  },
);

const signalPaths = [
  { name: "exec in place", preload: null, skip: !HAS_EXECVE && "needs process.execve" },
  { name: "spawn fallback", preload: "process.execve = undefined;\n", skip: false },
];

for (const { name, preload, skip } of signalPaths) {
  test(
    `SIGINT sent only to the shim reaches the binary (${name})`,
    { skip: (!POSIX_HOST && "POSIX signals only") || skip, timeout: 10000 },
    async (t) => {
      const dir = makeTempDir(t);
      const shim = installShimWithFakeBinary(
        dir,
        `#!/bin/sh\ntrap 'echo interrupted; exit ${INTERRUPTED_EXIT}' INT\necho ready\nwhile :; do sleep 0.1; done\n`,
      );
      const nodeArgs = [];
      if (preload) {
        fs.writeFileSync(path.join(dir, "preload.js"), preload);
        nodeArgs.push("--require", path.join(dir, "preload.js"));
      }

      // Own process group, so a timed-out run can be killed together with the looping binary.
      const child = spawn(process.execPath, [...nodeArgs, shim], { detached: true, env: { ...process.env, NODE_PATH: "" } });
      t.after(() => {
        try {
          process.kill(-child.pid, "SIGKILL");
        } catch {}
      });
      let stdout = "";
      child.stdout.on("data", (chunk) => {
        stdout += chunk;
        if (stdout.includes("ready")) {
          child.kill("SIGINT");
        }
      });
      const code = await new Promise((resolve) => child.on("exit", resolve));

      assert.equal(code, INTERRUPTED_EXIT);
      assert.match(stdout, /interrupted/);
    },
  );
}
