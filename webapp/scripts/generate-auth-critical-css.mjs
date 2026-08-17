import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import postcss from 'postcss';

const WEBAPP_ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const OUTPUT_PATH = join(WEBAPP_ROOT, 'src/css/auth-critical.css');
const ADDITIONS_PATH = join(WEBAPP_ROOT, 'src/css/auth-critical-additions.css');
const STYLE_SOURCES = [
  {
    path: 'src/css/openchat-theme.css',
    selectorIsCritical: (selector) => selector.trim() === '*' || /\.oc-auth(?:-|$)/.test(selector),
  },
  { path: 'src/css/catsco-ui-system.css', selectorIsCritical },
  { path: 'src/css/catsco-liquid-green.css', selectorIsCritical },
];

const CRITICAL_CLASS = /\.(?:oc-auth(?:-[\w-]+)?|oc-password-reset-code-row|oc-settings-secondary|oc-form-error|cc-inline-feedback(?:-[\w-]+)?|cc-toast(?:-[\w-]+)?|cc-confirm(?:-[\w-]+)?)(?=$|[\s.:#,[>+~])/;
const GLOBAL_SELECTOR = /^(?::root|\*|\*::before|\*::after|html(?:\[[^\]]+\])*(?:\s+\*)?|(?:html(?:\[[^\]]+\])*\s+)?(?:body|#root)(?:::[\w-]+)?|(?:button|input|textarea|select)(?:::[\w-]+|:[\w-]+(?:\([^)]*\))?)*|(?:input|textarea)::placeholder|strong|b|code|pre|kbd|samp|\[role="button"\]|\[tabindex\]:focus-visible)$/;
const GLOBAL_INPUT_GROUP = /^html(?:\[[^\]]+\])*\s+:is\(\s*(?:input|textarea|select)\b/;
const CRITICAL_KEYFRAME = /^(?:cc-liquid-|cc-toast-|cc-confirm-)/;
const GENERIC_FEEDBACK_SELECTOR = /^(?:\.oc-btn|\.oc-modal|\.oc-modal-overlay)$/;
const GENERIC_NESTED_SELECTOR = /^(?:input|textarea|select|button|\[role="button"\])(?:$|:)/;

function criticalClassIsInAuthOrGlobalContext(selector) {
  const match = selector.match(CRITICAL_CLASS);
  if (!match || match.index === undefined) return false;
  const prefix = selector.slice(0, match.index);
  const outerPrefix = prefix.includes(':is(') ? prefix.slice(0, prefix.lastIndexOf(':is(')) : prefix;
  return !/[.#][A-Za-z_-]/.test(outerPrefix);
}

function selectorIsCritical(selector) {
  const normalized = selector.trim().replace(/\s+/g, ' ');
  return criticalClassIsInAuthOrGlobalContext(normalized)
    || GENERIC_FEEDBACK_SELECTOR.test(normalized)
    || GLOBAL_SELECTOR.test(normalized)
    || GLOBAL_INPUT_GROUP.test(normalized);
}

function trimNestedSelectorLists(selector) {
  return selector.replace(/:is\(([^()]*)\)/g, (match, contents, offset) => {
    const outerSelector = selector.slice(0, offset);
    if (CRITICAL_CLASS.test(outerSelector) || GENERIC_FEEDBACK_SELECTOR.test(outerSelector.trim())) return match;
    const kept = contents.split(',').filter((part) => (
      selectorIsCritical(part) || GENERIC_NESTED_SELECTOR.test(part.trim())
    ));
    return kept.length > 0 ? `:is(${kept.join(',')})` : match;
  });
}

function filterNode(node, predicate) {
  if (node.type === 'rule') {
    const selectors = node.selectors.filter(predicate).map(trimNestedSelectorLists);
    if (selectors.length === 0) return null;
    const rule = node.clone();
    rule.selectors = selectors;
    return rule;
  }

  if (node.type !== 'atrule') return null;
  if (node.name.endsWith('keyframes')) {
    return CRITICAL_KEYFRAME.test(node.params) ? node.clone() : null;
  }
  if (!node.nodes) return null;

  const atRule = node.clone({ nodes: [] });
  node.nodes.forEach((child) => {
    const filtered = filterNode(child, predicate);
    if (filtered) atRule.append(filtered);
  });
  return atRule.nodes.length > 0 ? atRule : null;
}

function extractCriticalRules(source, predicate) {
  const root = postcss.parse(source);
  const filtered = postcss.root();
  root.nodes.forEach((node) => {
    const result = filterNode(node, predicate);
    if (result) filtered.append(result);
  });
  return filtered.toString().trim();
}

function atRuleContext(rule) {
  const context = [];
  let parent = rule.parent;
  while (parent && parent.type !== 'root') {
    if (parent.type === 'atrule') context.unshift(`@${parent.name} ${parent.params}`);
    parent = parent.parent;
  }
  return context.join('|');
}

function mergeRuleDeclarations(rules) {
  const declarations = [];
  rules.forEach((rule) => {
    const propertiesInRule = new Set();
    rule.nodes.forEach((node) => {
      if (node.type !== 'decl') return;
      if (!propertiesInRule.has(node.prop)) {
        propertiesInRule.add(node.prop);
        for (let index = declarations.length - 1; index >= 0; index -= 1) {
          if (declarations[index].prop === node.prop) declarations.splice(index, 1);
        }
      }
      declarations.push(node.clone());
    });
  });
  return declarations;
}

function collapseRepeatedRules(css) {
  const root = postcss.parse(css);
  const groups = new Map();
  root.walkRules((rule) => {
    const selector = rule.selector.replace(/\s+/g, ' ').trim();
    const key = `${atRuleContext(rule)}|${selector}`;
    const rules = groups.get(key) || [];
    rules.push(rule);
    groups.set(key, rules);
  });

  groups.forEach((rules) => {
    if (rules.length < 2) return;
    const last = rules.at(-1);
    last.replaceWith(last.clone({ nodes: mergeRuleDeclarations(rules) }));
    rules.slice(0, -1).forEach((rule) => rule.remove());
  });

  const atRules = [];
  root.walkAtRules((atRule) => atRules.push(atRule));
  atRules.reverse().forEach((atRule) => {
    if (atRule.nodes?.length === 0) atRule.remove();
  });
  return root.toString().trim();
}

export function generateAuthCriticalCss({ write = true } = {}) {
  const sections = STYLE_SOURCES.map(({ path, selectorIsCritical: predicate }) => {
    const source = readFileSync(join(WEBAPP_ROOT, path), 'utf8');
    const rules = extractCriticalRules(source, predicate);
    return rules ? `/* Source: ${path} */\n${rules}` : '';
  }).filter(Boolean);
  const additions = readFileSync(ADDITIONS_PATH, 'utf8').trim();
  const generated = [
    '/*',
    ' * Generated from the workspace CSS sources by scripts/generate-auth-critical-css.mjs.',
    ' * Do not edit this file directly. Edit a source stylesheet or auth-critical-additions.css instead.',
    ' */',
    collapseRepeatedRules(sections.join('\n\n')),
    `/* Source: ${relative(WEBAPP_ROOT, ADDITIONS_PATH)} */\n${additions}`,
    '',
  ].join('\n\n');

  if (write && readFileSync(OUTPUT_PATH, 'utf8') !== generated) {
    writeFileSync(OUTPUT_PATH, generated);
  }
  return generated;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  generateAuthCriticalCss();
}
