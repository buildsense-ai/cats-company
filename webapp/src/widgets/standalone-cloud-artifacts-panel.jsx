import React, { useState } from 'react';

import CloudArtifactsPanel from './cloud-artifacts-panel';
import { FilePreviewPanel, createCloudArtifactPreviewFile } from './chat-message';

function filePreviewPayload(file) {
  return {
    type: file?.type,
    name: file?.name,
    url: file?.url,
    file_key: file?.file_key,
    thumbnail: file?.thumbnail,
    width: file?.width,
    height: file?.height,
    mime_type: file?.mime_type,
    size: file?.size,
  };
}

/**
 * Artifacts panel for tasks without an active conversation.
 *
 * The conversation panel previews artifacts inside the side panel because the
 * conversation owns the artifact runtime bridge. Tasks without an active topic
 * have no conversation to host the preview, so this panel keeps the same
 * list/preview contract locally instead of sending every click to a new tab.
 */
export default function StandaloneCloudArtifactsPanel({
  agentUid,
  topicId,
  initialTab,
  tab,
  onTabChange,
  onClose,
  onOpenArtifact,
}) {
  const [previewFile, setPreviewFile] = useState(null);

  if (previewFile) {
    return (
      <FilePreviewPanel
        file={previewFile}
        onBack={() => setPreviewFile(null)}
        onClose={onClose}
        onOpenRemoteArtifactFullscreen={onOpenArtifact}
      />
    );
  }

  return (
    <CloudArtifactsPanel
      agentUid={agentUid}
      topicId={topicId}
      initialTab={initialTab}
      tab={tab}
      onTabChange={onTabChange}
      onClose={onClose}
      onPreviewArtifact={(artifact) => {
        if (!artifact) return;
        setPreviewFile(createCloudArtifactPreviewFile({
          ...artifact,
          agent_uid: artifact.agent_uid || agentUid,
        }));
      }}
      onPreviewFile={(file) => {
        if (!file) return;
        setPreviewFile(filePreviewPayload(file));
      }}
    />
  );
}
