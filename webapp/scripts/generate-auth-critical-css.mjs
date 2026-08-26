import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import postcss from 'postcss';

const WEBAPP_ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const OUTPUT_PATH = join(WEBAPP_ROOT, 'src/css/auth-critical.css');
const ADDITIONS_PATH = join(WEBAPP_ROOT, 'src/css/auth-critical-additions.css');
const OPENCHAT_AUTH_ROOT_PROPERTIES = new Set(['--oc-danger']);
const AUTH_ROOT_PROPERTIES = new Set([
  'color-scheme',
  '--cc-font-sans',
  '--cc-font-weight-regular',
  '--cc-font-weight-medium',
  '--cc-font-weight-semibold',
  '--cc-font-weight-bold',
  '--cc-bg',
  '--cc-panel',
  '--cc-border',
  '--cc-border-strong',
  '--cc-text',
  '--cc-text-secondary',
  '--cc-muted',
  '--cc-placeholder',
  '--cc-input-surface',
  '--cc-accent',
  '--cc-accent-hover',
  '--cc-accent-soft',
  '--cc-focus-ring',
  '--cc-danger',
  '--cc-code',
  '--cc-radius-md',
  '--cc-radius-lg',
  '--cc-liquid-edge',
  '--cc-scrollbar-panel-size',
  '--cc-scrollbar-size',
  '--cc-scrollbar-inset',
  '--cc-scrollbar-track',
  '--cc-scrollbar-thumb',
  '--cc-scrollbar-thumb-hover',
  '--cc-scrollbar-thumb-active',
  'scrollbar-color',
  'scrollbar-width',
  '--v3-primary',
  '--v3-text-muted',
]);
const AUTH_CRITICAL_KEYFRAMES = new Set([
  'cc-liquid-drift-a',
  'cc-liquid-drift-b',
  'cc-liquid-main-flow',
]);
const STYLE_SOURCES = [
  {
    path: 'src/css/openchat-theme.css',
    selectorIsCritical: openchatSelectorIsCritical,
    rootProperties: OPENCHAT_AUTH_ROOT_PROPERTIES,
  },
  {
    path: 'src/css/catsco-ui-system.css',
    selectorIsCritical,
    rootProperties: AUTH_ROOT_PROPERTIES,
    keyframes: AUTH_CRITICAL_KEYFRAMES,
  },
  {
    path: 'src/css/catsco-liquid-green.css',
    selectorIsCritical,
    rootProperties: AUTH_ROOT_PROPERTIES,
  },
];

const CRITICAL_CLASS = /\.(?:oc-auth(?:-[\w-]+)?|oc-password-reset-code-row|oc-settings-secondary|oc-form-error|cc-inline-feedback(?:-[\w-]+)?)(?=$|[\s.:#,[>+~])/;
const GLOBAL_SELECTOR = /^(?::root|\*|\*::before|\*::after|html(?:\[[^\]]+\])*(?:\s+\*)?|body|#root|(?:button|input|textarea|select)(?:::[\w-]+|:[\w-]+(?:\([^)]*\))?)*|(?:input|textarea)::placeholder|\[role="button"\]|\[tabindex\]:focus-visible)$/;
const GLOBAL_INPUT_GROUP = /^html(?:\[[^\]]+\])*\s+:is\(\s*(?:input|textarea|select)\b/;
const THEMED_DOCUMENT_SELECTOR = /^html(?:\[[^\]]+\])*\s+(?:body(?:::(?:before|after))?|#root)$/;
const MOBILE_TEXT_INPUT_SELECTOR = /^(?:input:not\(\[type\]\)|input\[type=["']?(?:text|search|email|password|tel|url|number)["']?\]|textarea|select)$/;
// Keep the visible scrollbar geometry and states in the auth shell. The
// source's verbose arrow reset is compacted below because display:none makes
// its size and appearance declarations redundant.
const GLOBAL_SCROLLBAR_SELECTOR = /^::-webkit-scrollbar(?:-track|-thumb(?::(?:hover|active))?|-button|-corner)?$/;
const GLOBAL_SCROLLBAR_BUTTON_SELECTOR = '::-webkit-scrollbar-button';
const AUTH_TOKEN_SELECTOR = /^(?::root|html(?:\[[^\]]+\])*)$/;
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
    || GLOBAL_SELECTOR.test(normalized)
    || normalized === ':focus-visible'
    || GLOBAL_SCROLLBAR_SELECTOR.test(normalized)
    || GLOBAL_INPUT_GROUP.test(normalized)
    || THEMED_DOCUMENT_SELECTOR.test(normalized)
    || MOBILE_TEXT_INPUT_SELECTOR.test(normalized);
}

function openchatSelectorIsCritical(selector) {
  const normalized = selector.trim().replace(/\s+/g, ' ');
  return normalized === '*'
    || normalized === ':root'
    || criticalClassIsInAuthOrGlobalContext(normalized)
    || normalized === ':focus-visible'
    || GLOBAL_SCROLLBAR_SELECTOR.test(normalized);
}

function trimNestedSelectorLists(selector) {
  return selector.replace(/:is\(([^()]*)\)/g, (match, contents, offset) => {
    const outerSelector = selector.slice(0, offset);
    if (CRITICAL_CLASS.test(outerSelector)) return match;
    const kept = contents.split(',').filter((part) => (
      selectorIsCritical(part) || GENERIC_NESTED_SELECTOR.test(part.trim())
    ));
    return kept.length > 0 ? `:is(${kept.join(',')})` : match;
  });
}

function filterNode(node, predicate, rootProperties, keyframes) {
  if (node.type === 'rule') {
    const selectors = node.selectors.filter(predicate).map(trimNestedSelectorLists);
    if (selectors.length === 0) return null;
    const rule = node.clone();
    rule.selectors = selectors;
    if (selectors.length === 1 && selectors[0].trim() === GLOBAL_SCROLLBAR_BUTTON_SELECTOR) {
      const displayDeclaration = rule.nodes.find((child) => (
        child.type === 'decl' && child.prop === 'display'
      ));
      if (displayDeclaration) {
        rule.removeAll();
        rule.append(displayDeclaration.clone());
      }
    }
    if (rootProperties && selectors.every((selector) => AUTH_TOKEN_SELECTOR.test(selector.trim()))) {
      const declarations = rule.nodes.filter((child) => (
        child.type === 'decl' && rootProperties.has(child.prop)
      ));
      if (declarations.length === 0) return null;
      rule.removeAll();
      declarations.forEach((declaration) => rule.append(declaration.clone()));
    }
    return rule;
  }

  if (node.type !== 'atrule') return null;
  if (node.name.endsWith('keyframes')) {
    return keyframes?.has(node.params.trim()) ? node.clone() : null;
  }
  if (!node.nodes) return null;

  const atRule = node.clone({ nodes: [] });
  node.nodes.forEach((child) => {
    const filtered = filterNode(child, predicate, rootProperties, keyframes);
    if (filtered) atRule.append(filtered);
  });
  return atRule.nodes.length > 0 ? atRule : null;
}

function extractCriticalRules(source, predicate, rootProperties, keyframes) {
  const root = postcss.parse(source);
  const filtered = postcss.root();
  root.nodes.forEach((node) => {
    const result = filterNode(node, predicate, rootProperties, keyframes);
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
  const sections = STYLE_SOURCES.map(({
    path,
    selectorIsCritical: predicate,
    rootProperties,
    keyframes,
  }) => {
    const source = readFileSync(join(WEBAPP_ROOT, path), 'utf8');
    const rules = extractCriticalRules(source, predicate, rootProperties, keyframes);
    return rules ? `/* Source: ${path} */\n${rules}` : '';
  }).filter(Boolean);
  const additions = readFileSync(ADDITIONS_PATH, 'utf8').trim();
  const generated = [
    '/*\n * Generated from the workspace CSS sources.\n * Do not edit directly; update a source stylesheet or auth-critical-additions.css.\n */',
    collapseRepeatedRules(sections.join('\n\n')),
    `/* Source: ${relative(WEBAPP_ROOT, ADDITIONS_PATH).replaceAll('\\', '/')} */\n${additions}`,
  ].join('\n\n').trimEnd().replace(/\r\n?/g, '\n') + '\n';

  if (write && readFileSync(OUTPUT_PATH, 'utf8') !== generated) {
    writeFileSync(OUTPUT_PATH, generated);
  }
  return generated;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  generateAuthCriticalCss();
}
