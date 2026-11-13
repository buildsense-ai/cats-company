import postcss from 'postcss';

// These surfaces can render inside the workspace or in a body-level portal.
export const secondarySurfaces = [
  '.oc-modal-overlay', '.name-dialog-overlay', '.oc-modal', '.name-dialog',
  'dialog', '[role="dialog"]', '[role="alertdialog"]', '[role="menu"]', '[role="listbox"]',
  '.settings-panel', '.collaboration-manager', '.v3-profile-popover',
  '.v3-model-menu', '.v3-custom-model-select-menu', '.v3-attachment-menu',
  '.cc-global-search', '.cc-skillhub-page', '.cc-apps-page', '.v3-file-preview-shell', '.cc-toast',
];

const violetTheme = 'html[data-theme="liquid"]:not([data-liquid-variant="green"])';
const surfaceSelector = `${violetTheme} :is(${secondarySurfaces.join(', ')})`;
const liquidSelector = /html\[data-theme\s*=\s*["']liquid["']\]/;

export const violetAccents = {
  '--cc-accent': '#5662d9',
  '--cc-accent-hover': '#4652c5',
  '--cc-accent-soft': 'rgba(86, 98, 217, 0.16)',
  '--cc-accent-text': '#4652c5',
  '--cc-brand-text-start': '#5662d9',
  '--cc-brand-text-end': '#5662d9',
  '--cc-online-icon': '#5662d9',
  '--cc-success-text': '#4652c5',
  '--oc-green-light': '#c9cdf4',
  '--oc-tab-active': '#5662d9',
};

export function liquidSecondaryTheme() {
  return {
    postcssPlugin: 'catsco-liquid-secondary-theme',
    Once(root) {
      const defaultTokens = root.nodes.find(node => node.type === 'rule' && node.selector === ':root');
      const liquidRules = [];
      root.walkRules(rule => {
        if (rule.selectors.some(selector => liquidSelector.test(selector))) liquidRules.push(rule);
      });
      for (const rule of liquidRules) {
        const themed = rule.selectors.filter(selector => liquidSelector.test(selector));
        const other = rule.selectors.filter(selector => !liquidSelector.test(selector));
        // Some components already share one rule between light and violet.
        const lightTargets = other
          .filter(selector => selector.startsWith('html:not([data-theme="dark"]):not([data-theme="liquid"]) '))
          .map(selector => selector.slice(selector.indexOf(' ') + 1));
        if (themed.every(selector => lightTargets.includes(selector.slice(selector.indexOf(' ') + 1)))) continue;
        const scopedRule = rule.clone({
          // Keep the original selector specificity while explicitly matching
          // the scope root itself, including in older Chromium versions.
          selectors: themed.map(selector => selector.replace(/^html(?=\[data-theme)/, 'html:where(:scope)')),
        });
        const scope = postcss.atRule({
          name: 'scope', params: `(:root) to (${surfaceSelector.replace(/^html/, ':scope')})`,
        });
        scope.append(scopedRule);
        rule.before(scope);
        if (other.length) rule.selectors = other;
        else rule.remove();
      }

      if (!root.source?.input.file?.endsWith('catsco-ui-system.css') || !defaultTokens) return;
      // Reuse the exact light palette and aliases, including future additions.
      // Only the secondary surfaces receive these tokens.
      const surfaceTokens = postcss.rule({ selector: surfaceSelector });
      defaultTokens.walkDecls(decl => surfaceTokens.append(decl.clone()));
      for (const [prop, value] of Object.entries(violetAccents)) surfaceTokens.append({ prop, value });
      surfaceTokens.append({ prop: 'color', value: 'var(--cc-text)' });
      root.append(surfaceTokens);
    },
  };
}
