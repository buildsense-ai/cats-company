type LegalPageProps = { kind: 'privacy' | 'terms' }

const content = {
  privacy: {
    kicker: 'PRIVACY POLICY DRAFT',
    title: '这是一份上线前说明，不是已生效的隐私政策。',
    intro: '本页面只用于说明正式隐私政策需要覆盖的主题，不构成最终法律文件，也不表示已经完成法律审阅。',
    sections: [
      ['需要确认的数据范围', '正式文本需要根据真实产品数据流，确认可能涉及的账号资料、用户主动提交内容、授权工作环境中的必要记录，以及网站运行所需信息。当前草案不列出尚未核实的数据类别。'],
      ['需要确认的授权边界', '正式文本需要说明用户或组织如何授权、查看和撤销工作环境权限。具体方式应以实际产品能力为准，不能由本草案提前承诺。'],
      ['需要确认的处理规则', '数据用途、保存位置、保留时间、删除方式、第三方服务和用户请求渠道，都要在运营主体、服务地区与技术流程明确后逐项审阅。'],
      ['当前联系状态', '如需讨论未来的数据处理计划，可以查看联系页面。当前联系表单只做本地格式检查，不会发送或保存内容。'],
    ],
  },
  terms: {
    kicker: 'TERMS OF USE DRAFT',
    title: '这是一份上线前说明，不是已生效的使用条款。',
    intro: '本页面只用于列出正式条款需要确认的主题，不构成服务协议，也不表示 CatsCo 已经确定运营主体、适用地区或完整服务范围。',
    sections: [
      ['需要确认的服务范围', '正式条款需要与实际产品能力、可用平台、支持方式、收费安排和企业方案保持一致。当前草案不把规划中的能力描述为已提供服务。'],
      ['需要确认的账号与授权规则', '正式条款需要说明账号责任，以及用户连接设备、应用、文件和组织资源时应具备的权限。具体规则要与真实授权流程一致。'],
      ['需要确认的使用边界', '正式文本需要明确禁止违法、侵害他人权益、绕过授权边界或破坏服务稳定性的使用方式，并结合适用地区完成审阅。'],
      ['需要确认的变更与联系机制', '条款更新、通知方式、争议处理和联系渠道都尚待运营主体与法律意见确认。本草案不承诺通知期限、管辖地或处理时限。'],
    ],
  },
} as const

export function LegalPage({ kind }: LegalPageProps) {
  const page = content[kind]
  return (
    <main id="main-content" className="content-page legal-page">
      <article className="content-shell legal-article">
        <header>
          <span className="page-kicker">{page.kicker}</span>
          <h1>{page.title}</h1>
          <p>{page.intro}</p>
          <div className="legal-draft-banner" role="note">
            <strong>状态：上线前草案</strong>
            <span>尚未根据真实运营主体、适用地区、产品数据流和法律意见定稿。</span>
          </div>
          <small><time dateTime="2026-08-06">草案更新：2026年8月6日</time> · 尚未完成法律审阅</small>
        </header>
        {page.sections.map(([title, text], index) => {
          const headingId = `${kind}-section-${index + 1}`
          return <section key={title} aria-labelledby={headingId}><h2 id={headingId}>{title}</h2><p>{text}</p></section>
        })}
        <aside className="legal-notice" aria-label="正式上线前必须确认">
          <strong>正式上线前必须确认</strong>
          <p>请由项目负责人先核实真实业务、运营主体、适用地区、数据流与第三方服务，再交由合格法律顾问审阅并发布正式文本。</p>
          <a href="/contact">查看当前联系说明</a>
        </aside>
      </article>
    </main>
  )
}
