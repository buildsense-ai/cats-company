import React, { useState } from 'react';
import ChatMessage, { FilePreviewPanel } from '../widgets/chat-message';

const demoUser = {
  uid: 38,
  name: 'ck',
  avatarUrl: '',
  isBot: false,
};

const demoBot = {
  uid: 110,
  name: 'CatsCo Analyst',
  avatarUrl: '',
  isBot: true,
};

const demoMessages = [
  {
    id: 1,
    from_uid: demoUser.uid,
    created_at: '2026-06-09T09:30:00Z',
    content: '帮我把这份班级数据做成一份可以发给教研组的报告，重点看异常值、平均分和改进建议。',
    content_blocks: [
      {
        type: 'text',
        text: '帮我把这份班级数据做成一份可以发给教研组的报告，重点看异常值、平均分和改进建议。',
      },
      {
        type: 'file',
        payload: {
          name: 'grade-summary.csv',
          url: '/demo-artifacts/grade-summary.csv',
          size: 1832,
          mime_type: 'text/csv',
        },
      },
    ],
  },
  {
    id: 2,
    from_uid: demoBot.uid,
    created_at: '2026-06-09T09:31:00Z',
    content: '报告已生成。我把结论、可疑样本和后续动作拆开了，下面这份 HTML 可以直接预览，也可以下载后发给同事。',
    content_blocks: [
      {
        type: 'thinking',
        thinking: '先读取表格，检查字段、缺失值和分数分布。',
      },
      {
        type: 'tool_use',
        id: 'tool-demo-read',
        name: 'read_file',
        input: {
          file_path: 'grade-summary.csv',
          focus: 'scores, missing values, outliers',
        },
      },
      {
        type: 'tool_result',
        tool_use_id: 'tool-demo-read',
        content: '读取 48 行学生记录，发现 3 个缺失项、2 个异常低分样本，整体均分 82.6。',
      },
      {
        type: 'thinking',
        thinking: '把统计摘要、建议和可复查数据合成一份工作流报告。',
      },
      {
        type: 'tool_use',
        id: 'tool-demo-report',
        name: 'generate_report',
        input: {
          format: 'html',
          audience: 'teaching group',
        },
      },
      {
        type: 'tool_result',
        tool_use_id: 'tool-demo-report',
        content: '生成 HTML 报告：teaching-report.html，并附带 CSV 数据明细。',
      },
      {
        type: 'text',
        text: '报告已生成。我把结论、可疑样本和后续动作拆开了，下面这份 HTML 可以直接预览，也可以下载后发给同事。',
      },
      {
        type: 'file',
        payload: {
          name: 'teaching-report.html',
          url: '/demo-artifacts/teaching-report.html',
          size: 6421,
          mime_type: 'text/html',
        },
      },
    ],
  },
  {
    id: 3,
    from_uid: demoUser.uid,
    created_at: '2026-06-09T09:35:00Z',
    content: '再给我一个只适合发群里的短版摘要。',
  },
  {
    id: 4,
    from_uid: demoBot.uid,
    created_at: '2026-06-09T09:35:30Z',
    content: '可以。短版摘要我放成 Markdown，方便你复制到群里，也能继续作为文件留在这条工作流里。',
    content_blocks: [
      {
        type: 'text',
        text: '可以。短版摘要我放成 Markdown，方便你复制到群里，也能继续作为文件留在这条工作流里。',
      },
      {
        type: 'file',
        payload: {
          name: '教研组摘要.md',
          url: '/demo-artifacts/teaching-summary.md',
          size: 913,
          mime_type: 'text/markdown',
        },
      },
    ],
  },
];

export default function WorkflowRichMediaDemo() {
  const [previewFile, setPreviewFile] = useState(null);

  return (
    <div className="v3-app v3-workflow-demo">
      <aside className="v3-workflow-demo-sidebar">
        <div>
          <div className="v3-brand-title">CatsCo</div>
          <div className="v3-workflow-demo-kicker">工作流消息模板</div>
        </div>
        <div className="v3-workflow-demo-thread active">
          <span>#</span>
          <div>
            <strong>教研报告协作</strong>
            <small>人、AI、文件、报告、工具结果</small>
          </div>
        </div>
        <div className="v3-workflow-demo-thread">
          <span>●</span>
          <div>
            <strong>合同审阅</strong>
            <small>PDF + 批注 + 风险列表</small>
          </div>
        </div>
        <div className="v3-workflow-demo-thread">
          <span>●</span>
          <div>
            <strong>周报生成</strong>
            <small>数据表 + 图表 + HTML</small>
          </div>
        </div>
      </aside>
      <main className="v3-main">
        <div className={`v3-message-workspace${previewFile ? ' has-preview' : ''}`}>
          <div className="v3-chat-column">
            <div className="v3-message-view">
              <div className="v3-chat-header">
                <div>
                  <div className="v3-chat-title">教研报告协作</div>
                  <div className="v3-chat-subtitle">模板数据 · 本地预览</div>
                </div>
              </div>
              <div className="v3-timeline">
                <div className="v3-timeline-inner">
                  {demoMessages.map((message) => {
                    const isSelf = message.from_uid === demoUser.uid;
                    const sender = isSelf ? demoUser : demoBot;
                    return (
                      <ChatMessage
                        key={message.id}
                        message={message}
                        isSelf={isSelf}
                        isGroup
                        senderName={sender.name}
                        senderAvatarUrl={sender.avatarUrl}
                        senderIsBot={sender.isBot}
                        showThinking
                        isConsecutive={false}
                        onPreviewFile={setPreviewFile}
                        activePreviewFile={previewFile}
                      />
                    );
                  })}
                </div>
              </div>
              <div className="v3-composer-wrap">
                <div className="v3-composer-box">
                  <textarea className="v3-composer-input" rows={1} disabled value="模板模式：这里不发送真实消息" readOnly />
                </div>
              </div>
            </div>
          </div>
          {previewFile && (
            <FilePreviewPanel file={previewFile} onClose={() => setPreviewFile(null)} />
          )}
        </div>
      </main>
    </div>
  );
}
