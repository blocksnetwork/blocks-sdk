"use strict";

const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { PLATFORM_PACKAGES } = require("../lib/platform");

const WRAPPER_DIR = path.join(__dirname, "..");
const IS_WINDOWS = process.platform === "win32";
const HOST = PLATFORM_PACKAGES[`${process.platform} ${process.arch}`];
const BAD_OPTION_EXIT = 9;

function run(command, args, options) {
  return spawnSync(command, args, { encoding: "utf8", shell: IS_WINDOWS, ...options });
}

function isolatedEnv(root) {
  const env = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !/^npm_/i.test(name)),
  );
  return {
    ...env,
    HOME: path.join(root, "home"),
    USERPROFILE: path.join(root, "home"),
    SHELL: "/bin/zsh",
    NODE_PATH: "",
    npm_config_userconfig: path.join(root, "npmrc"),
    npm_config_cache: path.join(root, "cache"),
    npm_config_offline: "true",
    npm_config_audit: "false",
    npm_config_fund: "false",
    npm_config_update_notifier: "false",
  };
}

function npm(args, { cwd, env }) {
  const result = run("npm", args, { cwd, env });
  assert.equal(result.status, 0, `npm ${args.join(" ")} failed:\n${result.stderr}`);
  return result.stdout;
}

// On Windows a copy of node.exe gives the same --version / bad-option behaviour.
function writeFakeBinary(file) {
  if (IS_WINDOWS) {
    fs.copyFileSync(process.execPath, file);
    return;
  }
  fs.writeFileSync(
    file,
    `#!/bin/sh\n[ "$1" = "--version" ] && { echo "${process.version}"; exit 0; }\n` +
      `echo "bad option: $1" >&2\nexit ${BAD_OPTION_EXIT}\n`,
    { mode: 0o755 },
  );
}

function packFakePlatformPackage(root, env) {
  const dir = path.join(root, "platform");
  const sourceDir = path.join(WRAPPER_DIR, "..", HOST.pkg.replace("@blocks-network/", ""));
  fs.mkdirSync(dir);
  fs.copyFileSync(path.join(sourceDir, "package.json"), path.join(dir, "package.json"));
  writeFakeBinary(path.join(dir, HOST.binary));
  return path.join(root, npm(["pack", "--pack-destination", root], { cwd: dir, env }).trim());
}

// Points the host's optional dependency at the local fake instead of the registry.
function packWrapper(root, env, platformTarball) {
  const dir = path.join(root, "wrapper");
  const isTestDir = (src) => path.relative(WRAPPER_DIR, src).split(path.sep)[0] === "test";
  fs.cpSync(WRAPPER_DIR, dir, { recursive: true, filter: (src) => !isTestDir(src) });
  const manifestPath = path.join(dir, "package.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  manifest.optionalDependencies = { [HOST.pkg]: `file:${platformTarball}` };
  fs.writeFileSync(manifestPath, JSON.stringify(manifest, null, 2));
  return path.join(root, npm(["pack", "--pack-destination", root], { cwd: dir, env }).trim());
}

function commandPath(prefix) {
  return IS_WINDOWS ? path.join(prefix, "blocks.cmd") : path.join(prefix, "bin", "blocks");
}

function listTree(dir) {
  return fs.readdirSync(dir, { recursive: true });
}

// npm leaves the emptied `@blocks-network` scope directory behind on uninstall.
function listFiles(dir) {
  return fs
    .readdirSync(dir, { recursive: true, withFileTypes: true })
    .filter((entry) => !entry.isDirectory())
    .map((entry) => path.join(path.relative(dir, entry.parentPath), entry.name));
}

test("published wrapper tarball contains the executable its bin field declares", () => {
  const [pack] = JSON.parse(run("npm", ["pack", "--dry-run", "--json"], { cwd: WRAPPER_DIR }).stdout);
  const manifest = JSON.parse(fs.readFileSync(path.join(WRAPPER_DIR, "package.json"), "utf8"));
  const bin = pack.files.find((file) => file.path === manifest.bin.blocks);

  assert.ok(bin, `${manifest.bin.blocks} is packed`);
  // A Windows checkout has no exec bit to pack; the package is published from Linux.
  if (!IS_WINDOWS) {
    assert.ok(bin.mode & 0o111, `${manifest.bin.blocks} is executable`);
  }
  assert.ok(!pack.files.some((file) => file.path.startsWith("test/")), "tests are not packed");
});

test("global install links blocks to the platform binary without touching HOME", { skip: !HOST && "unsupported host" }, (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "blocks-npm-install-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const env = isolatedEnv(root);
  const prefix = path.join(root, "prefix");
  fs.mkdirSync(env.HOME);

  const wrapperTarball = packWrapper(root, env, packFakePlatformPackage(root, env));
  const install = () => npm(["install", "--global", "--prefix", prefix, wrapperTarball], { cwd: root, env });
  const blocks = (args) => run(commandPath(prefix), args, { cwd: root, env });

  install();
  install();

  const version = blocks(["--version"]);
  assert.equal(version.status, 0, version.stderr);
  assert.equal(version.stdout.trim(), process.version);
  assert.equal(blocks(["--bad-option-for-test"]).status, BAD_OPTION_EXIT);
  assert.deepEqual(listTree(env.HOME), [], "install left files in HOME");

  npm(["uninstall", "--global", "--prefix", prefix, "@blocks-network/cli"], { cwd: root, env });

  assert.ok(!fs.existsSync(commandPath(prefix)), "uninstall removed the blocks command");
  assert.deepEqual(listFiles(prefix), [], "uninstall left files in the npm prefix");
  assert.deepEqual(listTree(env.HOME), [], "uninstall left files in HOME");
});
