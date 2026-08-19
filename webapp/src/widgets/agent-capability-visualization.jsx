import React, { useMemo, useState } from 'react';

const CAPABILITY_DOMAINS = [
  {
    id: 'analysis',
    label: '理解分析',
    shortLabel: '分析',
    keywords: ['analysis', 'analyze', 'review', 'reason', 'code', '分析', '审查', '推理', '代码'],
  },
  {
    id: 'tools',
    label: '工具执行',
    shortLabel: '执行',
    keywords: ['tool', 'browser', 'automation', 'execute', 'build', 'debug', '工具', '浏览器', '执行', '构建', '调试'],
  },
  {
    id: 'content',
    label: '内容产出',
    shortLabel: '产出',
    keywords: ['write', 'author', 'edit', 'document', 'pdf', 'content', '写作', '编辑', '文档', '内容'],
  },
  {
    id: 'research',
    label: '信息检索',
    shortLabel: '检索',
    keywords: ['research', 'search', 'data', 'crawl', 'source', '检索', '研究', '数据', '搜索', '资料'],
  },
  {
    id: 'collaboration',
    label: '协作交付',
    shortLabel: '协作',
    keywords: ['collaboration', 'workflow', 'project', 'share', 'export', '协作', '流程', '项目', '分享', '交付'],
  },
];

const ROLE_BASELINES = {
  code_review: [82, 72, 52, 62, 48],
  debugging: [78, 84, 42, 60, 48],
  writing: [66, 40, 88, 62, 55],
  research: [78, 52, 64, 90, 58],
  general: [64, 58, 62, 58, 60],
};

const ROLE_LABELS = {
  code_review: '代码审查',
  debugging: '问题排查',
  writing: '写作',
  research: '研究',
  general: '通用',
};

function skillLabel(skill) {
  return String(skill?.displayName || skill?.display_name || skill?.name || skill?.skillId || skill?.skill_id || skill?.id || 'Skill').trim();
}

function skillSearchText(skill) {
  return [
    skill?.skillId,
    skill?.skill_id,
    skill?.id,
    skillLabel(skill),
    skill?.description,
  ].filter(Boolean).join(' ').toLowerCase();
}

function resolveSkillDomain(skill, baselines) {
  const text = skillSearchText(skill);
  const matches = CAPABILITY_DOMAINS.map((domain, index) => ({
    index,
    score: domain.keywords.reduce((total, keyword) => total + (text.includes(keyword) ? 1 : 0), 0),
  }));
  const best = matches.reduce((current, candidate) => (
    candidate.score > current.score ? candidate : current
  ), matches[0]);
  if (best.score > 0) return best.index;
  return baselines.reduce((bestIndex, value, index, values) => (
    value > values[bestIndex] ? index : bestIndex
  ), 0);
}

export function buildAgentCapabilityProfile({ role, skills = [] } = {}) {
  const roleValue = String(role?.value || role || 'general');
  const baselines = ROLE_BASELINES[roleValue] || ROLE_BASELINES.general;
  const skillBuckets = CAPABILITY_DOMAINS.map(() => []);
  skills.forEach((skill) => {
    skillBuckets[resolveSkillDomain(skill, baselines)].push(skillLabel(skill));
  });
  const domains = CAPABILITY_DOMAINS.map((domain, index) => ({
    ...domain,
    baseScore: baselines[index],
    skillNames: skillBuckets[index],
    score: Math.min(96, baselines[index] + skillBuckets[index].length * 8),
  }));
  return {
    roleLabel: String(role?.label || ROLE_LABELS[roleValue] || ROLE_LABELS.general),
    skillCount: skills.length,
    score: Math.round(domains.reduce((sum, domain) => sum + domain.score, 0) / domains.length),
    domains,
  };
}

function polarPoint(radius, angle) {
  const radians = (angle - 90) * Math.PI / 180;
  return {
    x: 140 + radius * Math.cos(radians),
    y: 140 + radius * Math.sin(radians),
  };
}

function ringSectorPath(startAngle, endAngle, innerRadius, outerRadius) {
  const outerStart = polarPoint(outerRadius, startAngle);
  const outerEnd = polarPoint(outerRadius, endAngle);
  const innerEnd = polarPoint(innerRadius, endAngle);
  const innerStart = polarPoint(innerRadius, startAngle);
  const largeArc = endAngle - startAngle > 180 ? 1 : 0;
  return [
    `M ${outerStart.x} ${outerStart.y}`,
    `A ${outerRadius} ${outerRadius} 0 ${largeArc} 1 ${outerEnd.x} ${outerEnd.y}`,
    `L ${innerEnd.x} ${innerEnd.y}`,
    `A ${innerRadius} ${innerRadius} 0 ${largeArc} 0 ${innerStart.x} ${innerStart.y}`,
    'Z',
  ].join(' ');
}

export default function AgentCapabilityVisualization({ agentName, role, skills = [], compact = false }) {
  const profile = useMemo(() => buildAgentCapabilityProfile({ role, skills }), [role, skills]);
  const [activeDomainID, setActiveDomainID] = useState('');
  const activeDomain = profile.domains.find((domain) => domain.id === activeDomainID) || null;
  const centerLabel = activeDomain?.label || '能力结构';
  const centerValue = activeDomain?.score ?? profile.score;
  const centerDetail = activeDomain
    ? (activeDomain.skillNames.length > 0
        ? `${activeDomain.skillNames.length} 个 Skill 扩展`
        : '来自定位模板')
    : `${profile.skillCount} 个 Skill 已配置`;

  return (
    <section className={`cc-agent-capability-viz${compact ? ' is-compact' : ''}`} aria-labelledby="cc-agent-capability-viz-title">
      <header className="cc-agent-capability-viz-header">
        <div>
          <span>{compact ? '配置概览' : '创建结果'}</span>
          <h2 id="cc-agent-capability-viz-title">能力结构</h2>
        </div>
        <span className="cc-agent-capability-basis">可切换模型提高 Agent 能力</span>
      </header>

      <div className="cc-agent-capability-ring-wrap">
        <svg
          className="cc-agent-capability-ring"
          viewBox="0 0 280 280"
          role="img"
          aria-label={`${agentName || 'Agent'} 的能力结构，综合覆盖 ${profile.score}`}
        >
          <circle className="cc-agent-capability-ring-guide" cx="140" cy="140" r="122" />
          {profile.domains.map((domain, index) => {
            const sectorStart = index * 72 + 4;
            const sectorEnd = (index + 1) * 72 - 4;
            const baseEnd = sectorStart + (sectorEnd - sectorStart) * domain.baseScore / 100;
            const skillEnd = sectorStart + (sectorEnd - sectorStart) * Math.min(1, domain.skillNames.length / 3);
            const active = activeDomainID === domain.id;
            const muted = Boolean(activeDomainID && !active);
            return (
              <g
                key={domain.id}
                className={`cc-agent-capability-sector domain-${domain.id}${active ? ' is-active' : ''}${muted ? ' is-muted' : ''}`}
                tabIndex="0"
                role="button"
                aria-pressed={active}
                aria-label={`${domain.label} ${domain.score}，${domain.skillNames.length > 0 ? `${domain.skillNames.length} 个 Skill 扩展` : '来自定位模板'}`}
                onMouseEnter={() => setActiveDomainID(domain.id)}
                onMouseLeave={() => setActiveDomainID('')}
                onFocus={() => setActiveDomainID(domain.id)}
                onBlur={() => setActiveDomainID('')}
                onClick={() => setActiveDomainID((current) => current === domain.id ? '' : domain.id)}
              >
                <title>{`${domain.label} ${domain.score}；${domain.skillNames.length > 0 ? domain.skillNames.join('、') : '来自定位模板'}`}</title>
                <path className="cc-agent-capability-sector-track" d={ringSectorPath(sectorStart, sectorEnd, 68, 94)} />
                <path className="cc-agent-capability-sector-base" d={ringSectorPath(sectorStart, baseEnd, 68, 94)} />
                <path className="cc-agent-capability-skill-track" d={ringSectorPath(sectorStart, sectorEnd, 103, 122)} />
                {domain.skillNames.length > 0 && (
                  <path className="cc-agent-capability-skill-value" d={ringSectorPath(sectorStart, skillEnd, 103, 122)} />
                )}
              </g>
            );
          })}
          <circle className="cc-agent-capability-center-disc" cx="140" cy="140" r="54" />
          <text className="cc-agent-capability-center-label" x="140" y="120" textAnchor="middle">{centerLabel}</text>
          <text className="cc-agent-capability-center-value" x="140" y="151" textAnchor="middle">{centerValue}</text>
          <text className="cc-agent-capability-center-detail" x="140" y="171" textAnchor="middle">{centerDetail}</text>
        </svg>
        <div className="cc-agent-capability-legend" aria-hidden="true">
          <span><i className="is-role" />定位基础</span>
          <span><i className="is-skill" />Skill 扩展</span>
        </div>
      </div>

      <div className="cc-agent-capability-bars">
        <div className="cc-agent-capability-bars-heading">
          <h3>能力覆盖</h3>
          <span>{compact ? '配置估算' : profile.roleLabel}</span>
        </div>
        {profile.domains.map((domain) => {
          const active = activeDomainID === domain.id;
          const muted = Boolean(activeDomainID && !active);
          return (
            <button
              type="button"
              key={domain.id}
              className={`cc-agent-capability-bar domain-${domain.id}${active ? ' is-active' : ''}${muted ? ' is-muted' : ''}`}
              onMouseEnter={() => setActiveDomainID(domain.id)}
              onMouseLeave={() => setActiveDomainID('')}
              onFocus={() => setActiveDomainID(domain.id)}
              onBlur={() => setActiveDomainID('')}
              onClick={() => setActiveDomainID((current) => current === domain.id ? '' : domain.id)}
              aria-pressed={active}
              aria-label={`${domain.label} ${domain.score}`}
            >
              <span className="cc-agent-capability-bar-label">{domain.label}</span>
              <span className="cc-agent-capability-bar-track">
                <i style={{ '--cc-capability-value': `${domain.score}%` }} />
              </span>
              <strong>{domain.score}</strong>
            </button>
          );
        })}
      </div>

      <p className="cc-agent-capability-note">
        {compact
          ? '基于定位模板与 Skills 的配置估算，不代表模型性能。'
          : '覆盖度用于说明当前配置构成，不代表模型性能评测结果。'}
      </p>
    </section>
  );
}
