"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const { PLATFORM_PACKAGES } = require("../lib/platform");

const WRAPPER_DIR = path.join(__dirname, "..");
const NPM_DIR = path.join(WRAPPER_DIR, "..");
const PUBLISH_WORKFLOW = path.join(
  NPM_DIR, "..", "..", ".github", "workflows", "cli-npm-publish.yaml",
);
const UPGRADE_SOURCE = path.join(NPM_DIR, "..", "cmd", "upgrade.go");
const GO_NAMES = { win32: "windows", x64: "amd64" };

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function packageDir(pkg) {
  return pkg.replace("@blocks-network/", "");
}

const entries = Object.entries(PLATFORM_PACKAGES);

function goTarget(key) {
  return key.split(" ").map((name) => GO_NAMES[name] ?? name);
}

for (const [key, { pkg, binary }] of entries) {
  test(`${key} maps to a platform package that npm installs only on ${key}`, () => {
    const dir = path.join(NPM_DIR, packageDir(pkg));
    const manifest = readJson(path.join(dir, "package.json"));

    assert.equal(manifest.name, pkg);
    assert.equal(`${manifest.os.join()} ${manifest.cpu.join()}`, key);
    assert.deepEqual(manifest.files, [binary]);
    assert.ok(fs.existsSync(path.join(dir, "LICENSE")), `${pkg} ships a LICENSE`);
  });
}

test("wrapper optionalDependencies list exactly the mapped platform packages", () => {
  const { optionalDependencies } = readJson(path.join(WRAPPER_DIR, "package.json"));
  const mapped = entries.map(([, { pkg }]) => pkg).sort();

  assert.deepEqual(Object.keys(optionalDependencies).sort(), mapped);
});

test("publish workflow packages each mapped platform package from its own GOOS/GOARCH binary", () => {
  const workflow = fs.readFileSync(PUBLISH_WORKFLOW, "utf8");
  const packaged = Object.fromEntries(
    [...workflow.matchAll(/\["(cli-[^"]+)"\]="([^"]+)"/g)].map(([, dir, target]) => [dir, target]),
  );
  const mapped = Object.fromEntries(
    entries.map(([key, { pkg, binary }]) => [packageDir(pkg), [...goTarget(key), binary].join(" ")]),
  );

  assert.deepEqual(packaged, mapped);
});

test("publish workflow publishes exactly the mapped platform packages", () => {
  const workflow = fs.readFileSync(PUBLISH_WORKFLOW, "utf8");
  const loop = workflow.match(/for pkg_dir in ([^;]+); do\s+echo "Publishing/);
  assert.ok(loop, "publish loop not found in cli-npm-publish.yaml");

  const published = loop[1].trim().split(/\s+/).sort();
  const mapped = entries.map(([, { pkg }]) => packageDir(pkg)).sort();
  assert.deepEqual(published, mapped);
});

test("blocks upgrade installs the mapped platform package for each GOOS/GOARCH", () => {
  const source = fs.readFileSync(UPGRADE_SOURCE, "utf8");
  const upgradable = Object.fromEntries(
    [...source.matchAll(/"(\w+\/\w+)":\s*"(@blocks-network\/cli-[^"]+)"/g)].map(([, target, pkg]) => [target, pkg]),
  );
  const mapped = Object.fromEntries(entries.map(([key, { pkg }]) => [goTarget(key).join("/"), pkg]));

  assert.deepEqual(upgradable, mapped);
});
