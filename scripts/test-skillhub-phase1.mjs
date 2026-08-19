#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const rootDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const webappDir = path.join(rootDir, 'webapp');
const expectedGoTests = [
  'TestHandleListAgentsIncludesOwnedAndFriendBots',
  'TestHandleListMyBotsIncludesFriendBotsReadOnly',
  'TestBotDefinitionViewerSkillsRespectVisibilityAndRedactDefinition',
  'TestBotDefinitionSkillsResolvePrivateDisplayNamesWithoutExposingPackageData',
  'TestBotDefinitionSkillsUseUnifiedRevisionAndCanonicalOrder',
  'TestBotDefinitionSkillsRejectStaleRevisionAndInvalidRefs',
  'TestBotDefinitionSkillsOwnerAndRuntimeScope',
  'TestFullBotDefinitionResponseIncludesSkills',
  'TestBotDefinitionSkillsNoopKeepsUnifiedRevision',
  'TestSetBotSkillsVisibility',
  'TestSetBotSkillsVisibilityRejectsInvalidValueAndNonOwner',
  'TestSkillHubPrivateMetadataUsesBotCredentialsAndReturnsOnlyRequestedNames',
  'TestSkillHubProxyForwardsCatalogueQuery',
  'TestSkillHubThinToolRPCAuthorization',
];
const goTestPattern = [
  '^(',
  expectedGoTests.join('|'),
  '|TestSkillHubProxy.*',
  ')$',
].join('');

const listed = runCapture(
  'list required CatsCo SkillHub tests',
  'go',
  ['test', './server', '-list', goTestPattern],
);
const listedTests = new Set(
  listed.split(/\r?\n/).map(line => line.trim()).filter(line => line.startsWith('Test')),
);
for (const testName of expectedGoTests) {
  if (!listedTests.has(testName)) {
    console.error(`[skillhub-phase1] Missing mandatory backend test: ${testName}`);
    process.exit(1);
  }
}

run(
  'CatsCo SkillHub owner, friend, visibility, and proxy contracts',
  'go',
  ['test', './server', '-run', goTestPattern, '-count=1'],
);
run(
  'CatsCo WebApp SkillHub workflow',
  process.execPath,
  [path.join(webappDir, 'node_modules', 'vitest', 'vitest.mjs'), 'run', 'src/views/skillhub-view.test.jsx'],
  { cwd: webappDir },
);

console.log('[skillhub-phase1] Mandatory CatsCo SkillHub regression passed.');

function run(name, command, args, options = {}) {
  console.log(`[skillhub-phase1] ${name}`);
  const result = spawnSync(command, args, {
    cwd: options.cwd || rootDir,
    env: process.env,
    stdio: 'inherit',
    shell: false,
    timeout: 180_000,
  });
  assertPassed(name, result);
}

function runCapture(name, command, args) {
  console.log(`[skillhub-phase1] ${name}`);
  const result = spawnSync(command, args, {
    cwd: rootDir,
    env: process.env,
    encoding: 'utf8',
    shell: false,
    timeout: 180_000,
  });
  assertPassed(name, result);
  return result.stdout || '';
}

function assertPassed(name, result) {
  if (result.error) {
    console.error(`[skillhub-phase1] ${name} failed: ${result.error.message}`);
    process.exit(1);
  }
  if (result.signal) {
    console.error(`[skillhub-phase1] ${name} terminated by ${result.signal}`);
    process.exit(1);
  }
  if (result.status !== 0) {
    if (result.stderr) console.error(result.stderr);
    process.exit(result.status || 1);
  }
}
