import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import AgentCapabilityVisualization, { buildAgentCapabilityProfile } from './agent-capability-visualization';

describe('Agent capability visualization', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  test('builds a deterministic profile from the assistant role and selected Skills', () => {
    const profile = buildAgentCapabilityProfile({
      role: { value: 'writing', label: '写作' },
      skills: [
        { id: 'pdf-author', name: 'PDF Author', description: 'Write structured documents' },
        { id: 'browser-search', name: 'Browser Search', description: 'Research sources' },
      ],
    });

    expect(profile.roleLabel).toBe('写作');
    expect(profile.skillCount).toBe(2);
    expect(profile.domains.find((domain) => domain.id === 'content')).toMatchObject({
      score: 96,
      skillNames: ['PDF Author'],
    });
    expect(profile.domains.find((domain) => domain.id === 'research').skillNames)
      .toEqual(['Browser Search']);
  });

  test('keeps the comparison bars visible in the compact management view', async () => {
    await act(async () => {
      root.render(
        <AgentCapabilityVisualization
          agentName="研究助手"
          role="research"
          skills={[]}
          compact
        />,
      );
    });

    expect(container.querySelectorAll('.cc-agent-capability-bar')).toHaveLength(5);
    expect(container.textContent).toContain('配置估算');
    expect(container.textContent).toContain('不代表模型性能');
  });

  test('links ring and bar hover states without relying on color alone', async () => {
    await act(async () => {
      root.render(
        <AgentCapabilityVisualization
          agentName="审查助手"
          role={{ value: 'code_review', label: '代码审查助手' }}
          skills={[{ id: 'code-review', name: '代码审查', description: 'Review code quality' }]}
        />,
      );
    });

    expect(container.querySelectorAll('.cc-agent-capability-sector')).toHaveLength(5);
    expect(container.querySelectorAll('.cc-agent-capability-bar')).toHaveLength(5);
    expect(container.textContent).toContain('不代表模型性能评测结果');

    const analysisBar = container.querySelector('.cc-agent-capability-bar.domain-analysis');
    await act(async () => Simulate.mouseEnter(analysisBar));

    expect(analysisBar.classList.contains('is-active')).toBe(true);
    expect(container.querySelector('.cc-agent-capability-sector.domain-analysis').classList.contains('is-active')).toBe(true);
    expect(container.querySelector('.cc-agent-capability-center-label').textContent).toBe('理解分析');

    await act(async () => Simulate.mouseLeave(analysisBar));
    expect(container.querySelector('.cc-agent-capability-center-label').textContent).toBe('能力结构');
  });
});
