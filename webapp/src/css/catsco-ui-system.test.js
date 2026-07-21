import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/css/catsco-ui-system.css'), 'utf8');

const ruleFor = (selector) => css.match(
  new RegExp(`(?:^|\\r?\\n)${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{[^}]*\\}`),
)?.[0] || '';

describe('CatsCo shell styling', () => {
  it('uses the supplied transparent brand image wherever the shared mark is rendered', () => {
    const brandRule = ruleFor('.catsco-brand-mark');

    expect(existsSync(resolve(process.cwd(), 'public/catsco-brand-asasda.png'))).toBe(true);
    expect(brandRule).toContain('width: 48px;');
    expect(brandRule).toContain("url('/catsco-brand-asasda.png')");
    expect(brandRule).toContain('background');
    expect(brandRule).toContain('contain no-repeat');
    expect(brandRule).toContain('-webkit-mask: none;');
    expect(brandRule).toContain('mask: none;');
  });

  it('keeps sidebar chrome fixed while the navigation list owns overflow', () => {
    const headerRule = ruleFor('.v3-sidebar-header');
    const toolsRule = ruleFor('.cc-sidebar-tools');
    const listRule = ruleFor('.v3-chat-list');
    const footerRule = ruleFor('.v3-profile-footer');

    expect(headerRule).toContain('flex: 0 0 56px;');
    expect(toolsRule).toContain('flex: 0 0 auto;');
    expect(listRule).toContain('min-height: 0;');
    expect(listRule).toContain('flex: 1 1 auto;');
    expect(listRule).toContain('overflow-y: auto;');
    expect(footerRule).toContain('flex: 0 0 auto;');
  });

  it('uses a lighter heading weight on the empty task screen', () => {
    const markRule = ruleFor('.cc-empty-task-mark');

    expect(markRule).toContain('width: 128px;');
    expect(markRule).toContain('height: 56px;');
    expect(ruleFor('.cc-empty-task h1')).toContain('font-weight: 500;');
  });
});
