import React from 'react';
import { BookOpen, Cpu, Layers, Server, ShieldCheck, Users } from 'lucide-react';
import './enterprise-team.css';
import { teamPlanOffer } from './plan-offers';


const benefits = [
  [Server, '独享服务器', '4 核 CPU · 8 GB 内存，团队独立使用'],
  [Layers, '团队经验与 Skill 共同沉淀', '把工作中积累的经验和 Skill 留给整个团队复用'],
  [BookOpen, '共享知识库', '团队资料集中管理，成员共同查阅与使用'],
  [ShieldCheck, '团队权限管理', '管理成员权限与团队资源的访问范围'],
];

export default function EnterpriseTeam({ onSelectPlan, plan = teamPlanOffer }) {
  return <section className="enterprise-team" aria-labelledby="enterprise-team-title">
    <div className="enterprise-team-offer">
      <span className="enterprise-team-eyebrow">CATSCO TEAM</span>
      <h3 id="enterprise-team-title">团队版</h3>
      <p>适合团队共同使用与管理 AI 员工。</p>
      <div className="enterprise-team-price"><span>¥</span><strong>599</strong><small>/ 月</small></div>
      <button type="button" onClick={() => onSelectPlan(plan)}>获取 CatsCo 团队版</button>
      <dl className="enterprise-team-allowances">
        <div><dt><Users size={16} aria-hidden="true" />团队成员</dt><dd>最多 10 人</dd></div>
        <div><dt><Cpu size={16} aria-hidden="true" />Token 额度</dt><dd>最高 30 亿 <span>Token</span></dd></div>
      </dl>
    </div>
    <div className="enterprise-team-benefits">
      <h4>团队协作，从同一个工作空间开始</h4>
      <ul>{benefits.map(([Icon, title, description]) => <li key={title}>
        <span className="enterprise-team-icon"><Icon size={19} aria-hidden="true" /></span>
        <div><strong>{title}</strong><p>{description}</p></div>
      </li>)}</ul>
    </div>
  </section>;
}
