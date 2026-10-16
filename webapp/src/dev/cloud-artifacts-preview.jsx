import React, { useEffect, useState } from 'react';
import { Cloud, Maximize2, Minimize2 } from 'lucide-react';
import { api } from '../api';
import CloudArtifactsPanel from '../widgets/cloud-artifacts-panel';
import { createCloudArtifactPreviewFile, FilePreviewPanel } from '../widgets/chat-message';
import './cloud-artifacts-preview.css';

const DEMO_AGENT_UID = 440;
const DEMO_TOPIC_ID = 'demo-cloud-topic';
const DEMO_ARTIFACT_URL = '/demo-artifacts/teaching-report.html';

const demoArtifacts = [
  {
    id: 'teaching-report',
    title: '教学分析报告',
    url: DEMO_ARTIFACT_URL,
    kind: 'mini_app',
    publish_version: 3,
    agent_uid: DEMO_AGENT_UID,
    source_topic_id: DEMO_TOPIC_ID,
    updated_at: '2026-10-16T05:30:00Z',
    tags: ['报告', '演示'],
  },
  {
    id: 'lesson-dashboard',
    title: '课程进度看板',
    url: DEMO_ARTIFACT_URL,
    kind: 'html',
    publish_version: 2,
    agent_uid: DEMO_AGENT_UID,
    source_topic_id: DEMO_TOPIC_ID,
    updated_at: '2026-10-15T09:20:00Z',
    tags: ['课程'],
  },
];

const demoFiles = [
  {
    id: 1,
    name: '教学分析报告.html',
    url: DEMO_ARTIFACT_URL,
    mime_type: 'text/html',
    type: 'file',
    created_at: '2026-10-16T05:30:00Z',
  },
];

function installDemoApi() {
  const original = {
    getCloudArtifacts: api.getCloudArtifacts,
    getCloudArtifactTags: api.getCloudArtifactTags,
    getTopicFiles: api.getTopicFiles,
    listArtifactApps: api.listArtifactApps,
  };

  api.getCloudArtifacts = async (_agentUid, status = 'active') => ({
    artifacts: status === 'deleted' ? [] : demoArtifacts,
    viewer_relation: 'owner',
    can_publish: true,
    publish_mode: 'immediate',
  });
  api.getCloudArtifactTags = async () => ({
    tags: [
      { tag: '报告', count: 1 },
      { tag: '演示', count: 1 },
      { tag: '课程', count: 1 },
    ],
  });
  api.getTopicFiles = async () => ({
    files: demoFiles,
    has_more: false,
    next_before_id: 0,
    next_before_created_at: '',
  });
  api.listArtifactApps = async () => ({
    apps: demoArtifacts.map((artifact) => ({
      id: artifact.id,
      title: artifact.title,
      name: artifact.title,
      url: DEMO_ARTIFACT_URL,
    })),
  });

  return () => {
    Object.assign(api, original);
  };
}

export default function CloudArtifactsPreview() {
  const [expanded, setExpanded] = useState(false);
  const [previewFile, setPreviewFile] = useState(null);
  const [apiReady, setApiReady] = useState(false);

  useEffect(() => {
    const restore = installDemoApi();
    setApiReady(true);
    return restore;
  }, []);

  const openArtifact = (artifact) => {
    setPreviewFile(createCloudArtifactPreviewFile({
      ...artifact,
      agent_uid: DEMO_AGENT_UID,
      url: DEMO_ARTIFACT_URL,
    }));
  };

  const closePanel = () => {
    setPreviewFile(null);
    setExpanded(false);
  };

  return (
    <main className="cc-cloud-artifacts-demo">
      <header className="cc-cloud-artifacts-demo-topbar">
        <div className="cc-cloud-artifacts-demo-brand">
          <span className="catsco-brand-mark" aria-hidden="true" />
          <span>CatsCo</span>
        </div>
        <div className="cc-cloud-artifacts-demo-title">
          <Cloud size={17} aria-hidden="true" />
          <span>云内容面板 Demo</span>
        </div>
        <span className="cc-cloud-artifacts-demo-hint">点击右上角展开 / 缩小</span>
      </header>

      <div className={`v3-message-workspace has-preview cc-cloud-artifacts-demo-workspace${expanded ? ' is-cloud-panel-expanded' : ''}`}>
        <section className="cc-cloud-artifacts-demo-context" aria-label="聊天上下文演示">
          <div className="cc-cloud-artifacts-demo-context-copy">
            <span className="cc-cloud-artifacts-demo-kicker">当前聊天</span>
            <h1>保留聊天，上下文不丢失</h1>
            <p>云内容停靠在右侧时可以快速查看；展开后占满主工作区，缩小即可回到这里。</p>
          </div>
          <div className="cc-cloud-artifacts-demo-message">
            <span className="catsco-brand-mark" aria-hidden="true" />
            <div>
              <strong>虚拟员工</strong>
              <p>这是聊天区域的演示占位，不会影响云内容面板的内部状态。</p>
            </div>
          </div>
        </section>

        <div className="v3-file-preview-shell cc-cloud-artifacts-demo-panel">
          {!apiReady ? (
            <div className="cloud-artifacts-status" role="status">正在准备演示数据…</div>
          ) : previewFile ? (
            <FilePreviewPanel
              file={previewFile}
              onBack={() => setPreviewFile(null)}
              onClose={closePanel}
              onOpenRemoteArtifactFullscreen={() => {}}
              workspaceFullscreen={expanded}
              onToggleWorkspaceFullscreen={() => setExpanded((current) => !current)}
              allowWorkspaceFullscreen
            />
          ) : (
            <CloudArtifactsPanel
              agentUid={DEMO_AGENT_UID}
              topicId={DEMO_TOPIC_ID}
              initialTab="active"
              onClose={closePanel}
              onPreviewArtifact={openArtifact}
              onPreviewFile={(file) => setPreviewFile(file)}
              workspaceFullscreen={expanded}
              onToggleWorkspaceFullscreen={() => setExpanded((current) => !current)}
              allowWorkspaceFullscreen
            />
          )}
        </div>
      </div>

      <footer className="cc-cloud-artifacts-demo-footer">
        <button type="button" onClick={() => setExpanded((current) => !current)}>
          {expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
          {expanded ? '缩小面板' : '展开面板'}
        </button>
        <span>演示数据仅用于查看布局和交互</span>
      </footer>
    </main>
  );
}
