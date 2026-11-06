import React from 'react';
import { ArrowUpRight, Check, Layers, Settings2, Users, Workflow, Sparkles, MessageSquare } from 'lucide-react';
import './enterprise-services.css';

const services = [
  [Settings2, '环境准备', '企业使用环境的基础配置、账号与运行检查'],
  [Users, '团队启动', '面向负责人和实际使用者的启动培训'],
  [Workflow, '场景梳理', '梳理一个边界清晰、可验证的首批业务场景'],
  [Sparkles, 'Skill 初始化', '初始化约定范围内的首批 Skill'],
  [MessageSquare, '上线支持', '上线初期反馈处理与约定范围内的小幅调整'],
];

export default function EnterpriseReference() {
  return <section className="relay-access-enterprise-card enterprise-reference" aria-labelledby="enterprise-reference-title">
    <div className="enterprise-reference-offer">
      <span className="enterprise-reference-eyebrow">CATSCO FOR BUSINESS</span>
      <h3 id="enterprise-reference-title">让 AI 员工<br />加入你的团队</h3>
      <span className="enterprise-reference-plan">Business Start · 企业落地服务</span>
      <p>帮助一个团队或一个明确业务场景真正开始使用 AI 员工，从环境准备到首个场景上线。</p>
      <div className="enterprise-reference-purchase">
        <div className="enterprise-reference-price"><span>¥</span><strong>4,999</strong><small>/ 月起</small></div>
        <a href="/contact?topic=enterprise&amp;service=business-start&amp;source=pricing">咨询企业落地<ArrowUpRight size={16} /></a>
      </div>
    </div>
    <div className="enterprise-reference-visual">
      <div className="enterprise-reference-relief" aria-hidden="true">
        <div className="enterprise-reference-paper"><i /><i /><i /></div>
        <div className="enterprise-reference-flow"><i /><i /><i /></div>
      </div>
      <div className="enterprise-reference-caption"><span>基础服务范围</span><h4>从准备到上线，一起落地。</h4></div>
      <div className="enterprise-reference-workspace">
        <header><span><Layers size={18} />团队 AI 工作空间</span><span>Business Start</span></header>
        <ul>{services.map(([Icon, title, description]) => <li key={title}>
          <span className="enterprise-reference-service-icon"><Icon size={18} /></span>
          <div><strong>{title}</strong><p>{description}</p></div>
          <Check size={16} aria-hidden="true" />
        </li>)}</ul>
      </div>
      <div className="enterprise-reference-custom"><strong>高级企业服务需另行咨询</strong><p>多部门流程改造、复杂系统集成、专属数据治理、私有化、安全合规、长期驻场和大规模 Skill 开发，均按范围与交付结果单独确认。</p></div>
    </div>
  </section>;
}
