import React, { useState } from 'react';
import { Bookmark } from 'lucide-react';
import { fileAnnotationTargetSummary, normalizeFileAnnotations } from '../utils/file-annotations';

const labels = { image: '图片区域', video: '视频帧', pdf: 'PDF 页面', text: '文本', cells: '单元格' };

export default function FileAnnotationCard({ value }) {
  const [expanded, setExpanded] = useState(false);
  const doc = normalizeFileAnnotations(value);
  if (!doc?.annotations?.length) return null;
  const visible = expanded ? doc.annotations : doc.annotations.slice(0, 3);
  return (
    <div className="v3-gateway-annotation-card" data-contract={doc.contract_version}>
      <div className="v3-gateway-annotation-head">
        <span className="v3-gateway-annotation-title"><Bookmark size={14} aria-hidden="true" />文件批注 · {doc.source.name}</span>
        {doc.annotations.length > 3 && <button type="button" className="v3-gateway-annotation-toggle"
          aria-expanded={expanded} onClick={() => setExpanded(current => !current)}>
          {expanded ? '收起' : `全部 ${doc.annotations.length} 条`}
        </button>}
      </div>
      <ul className="v3-gateway-annotation-list">
        {visible.map(annotation => <li key={annotation.id} className="v3-gateway-annotation-item">
          <span className="v3-gateway-annotation-kind">{labels[annotation.kind]}</span>
          <span className="v3-gateway-annotation-copy"><em>{fileAnnotationTargetSummary(annotation.kind, annotation.target)}</em><span>{annotation.body}</span></span>
        </li>)}
      </ul>
    </div>
  );
}
