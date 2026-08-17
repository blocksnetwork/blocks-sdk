#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const defaultSdkRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export async function checkAgentDistribution(sdkRoot = defaultSdkRoot) {
  const skillsRoot = path.join(sdkRoot, 'skills');
  const canonicalSchemaPath = path.join(sdkRoot, 'schemas', 'agent-card.schema.json');
  const packagedSchemaPath = path.join(
    skillsRoot,
    'blocks-network',
    'references',
    'agent-card.schema.json',
  );

  const plugin = JSON.parse(await readFile(path.join(sdkRoot, 'plugin.json'), 'utf8'));
  validatePluginManifest(plugin);

  const skillDirectories = (await readdir(skillsRoot, { withFileTypes: true }))
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name);

  const canonicalLicense = await readFile(path.join(sdkRoot, 'LICENSE'));
  for (const skillName of ['blocks-getstarted', 'blocks-network']) {
    assert(skillDirectories.includes(skillName), `missing skills/${skillName}`);
    const skillPath = path.join(skillsRoot, skillName, 'SKILL.md');
    const source = await readFile(skillPath, 'utf8');
    const frontmatter = source.match(/^---\n([\s\S]*?)\n---/);
    assert(frontmatter, `${relative(sdkRoot, skillPath)} is missing YAML frontmatter`);
    assert(
      new RegExp(`^name:\\s*${escapeRegExp(skillName)}\\s*$`, 'm').test(frontmatter[1]),
      `${relative(sdkRoot, skillPath)} frontmatter name must match its directory`,
    );
    assert(
      /^description:\s*\S.+$/m.test(frontmatter[1]),
      `${relative(sdkRoot, skillPath)} frontmatter must include a description`,
    );
    const packagedLicensePath = path.join(skillsRoot, skillName, 'LICENSE');
    const packagedLicense = await readFile(packagedLicensePath);
    assert(
      canonicalLicense.equals(packagedLicense),
      `${relative(sdkRoot, packagedLicensePath)} must match the repository LICENSE`,
    );
  }

  const canonicalSchema = await readFile(canonicalSchemaPath);
  const packagedSchema = await readFile(packagedSchemaPath);
  assert(
    canonicalSchema.equals(packagedSchema),
    [
      `${relative(sdkRoot, packagedSchemaPath)} is stale`,
      `copy ${relative(sdkRoot, canonicalSchemaPath)} to the packaged skill reference`,
      `canonical sha256=${digest(canonicalSchema)}`,
      `packaged sha256=${digest(packagedSchema)}`,
    ].join('; '),
  );

  for (const skillName of ['blocks-getstarted', 'blocks-network']) {
    const skillRoot = path.join(skillsRoot, skillName);
    const siblingSkillNames = ['blocks-getstarted', 'blocks-network'].filter(
      (candidate) => candidate !== skillName,
    );
    for (const markdownPath of await findMarkdownFiles(skillRoot)) {
      const source = await readFile(markdownPath, 'utf8');
      for (const siblingSkillName of siblingSkillNames) {
        assert(
          !source.includes(`../${siblingSkillName}/`),
          `${relative(sdkRoot, markdownPath)} references sibling skill ${siblingSkillName}`,
        );
      }
      for (const target of localMarkdownTargets(source)) {
        const resolved = path.resolve(path.dirname(markdownPath), target);
        assert(
          isWithin(skillRoot, resolved),
          `${relative(sdkRoot, markdownPath)} references a file outside its skill: ${target}`,
        );
        await readFile(resolved).catch(() => {
          throw new Error(
            `${relative(sdkRoot, markdownPath)} has a missing local reference: ${target}`,
          );
        });
      }
    }
  }
}

async function findMarkdownFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...(await findMarkdownFiles(entryPath)));
    else if (entry.isFile() && entry.name.endsWith('.md')) files.push(entryPath);
  }
  return files;
}

function localMarkdownTargets(source) {
  const targets = [];
  const patterns = [/\[[^\]]+\]\(([^)]+)\)/g, /^\[[^\]]+\]:\s*(\S+)/gm];
  for (const pattern of patterns) {
    for (const match of source.matchAll(pattern)) {
      const target = match[1].replace(/^<|>$/g, '').split('#', 1)[0];
      if (target && !/^(?:https?:|mailto:)/.test(target)) targets.push(target);
    }
  }
  return targets;
}

function isWithin(parent, child) {
  const relativePath = path.relative(parent, child);
  return (
    relativePath !== '..' &&
    !relativePath.startsWith(`..${path.sep}`) &&
    !path.isAbsolute(relativePath)
  );
}

function digest(contents) {
  return createHash('sha256').update(contents).digest('hex');
}

function relative(sdkRoot, filePath) {
  return path.relative(sdkRoot, filePath);
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function validatePluginManifest(plugin) {
  const allowedFields = new Set([
    '$schema',
    'name',
    'version',
    'description',
    'author',
    'homepage',
    'repository',
    'license',
    'keywords',
    'extensions',
  ]);
  assert(isObject(plugin), 'plugin.json must contain an object');
  assert(
    Object.keys(plugin).every((field) => allowedFields.has(field)),
    'plugin.json contains an unsupported field',
  );
  assert(
    plugin.$schema === 'https://agent-plugins.org/schemas/1.0.0/plugin.schema.json',
    'plugin.json must target Agent Plugins 1.0.0',
  );
  assert(
    typeof plugin.name === 'string' &&
      plugin.name.length <= 64 &&
      /^(?!.*(?:--|\.\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/.test(plugin.name),
    'plugin.json name does not satisfy Agent Plugins 1.0.0 naming rules',
  );
  for (const field of ['version', 'description', 'homepage', 'repository', 'license']) {
    assert(
      plugin[field] === undefined || typeof plugin[field] === 'string',
      `${field} must be a string`,
    );
  }
  if (plugin.author !== undefined) {
    assert(isObject(plugin.author), 'author must be an object');
    assert(
      Object.keys(plugin.author).every((field) => ['name', 'email', 'url'].includes(field)),
      'author contains an unsupported field',
    );
    for (const field of ['name', 'email', 'url']) {
      assert(
        plugin.author[field] === undefined || typeof plugin.author[field] === 'string',
        `author.${field} must be a string`,
      );
    }
  }
  assert(
    plugin.keywords === undefined ||
      (Array.isArray(plugin.keywords) &&
        plugin.keywords.every((keyword) => typeof keyword === 'string')),
    'keywords must be an array of strings',
  );
  assert(
    plugin.extensions === undefined ||
      (isObject(plugin.extensions) &&
        Object.values(plugin.extensions).every((extension) => isObject(extension))),
    'extensions must map namespaces to objects',
  );
}

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

const isMain = process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1]);
if (isMain) {
  try {
    await checkAgentDistribution();
    console.log('Agent Plugin and Agent Skills distribution structure is valid.');
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.error(`Agent distribution check failed: ${message}`);
    process.exitCode = 1;
  }
}
