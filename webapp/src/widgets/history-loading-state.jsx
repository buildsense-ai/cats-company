import React from 'react';
import '../css/history-loading-state.css';
import useLoadingHandoff from '../utils/use-loading-handoff';
import LayeredLoadingMotion from './layered-loading-motion';

export default function HistoryLoadingState() {
  const ref = useLoadingHandoff();
  return (
    <div ref={ref} className="v3-history-state cc-history-loading" role="status" aria-live="polite">
      <LayeredLoadingMotion kind="chat" />
      <span>正在读取对话<span className="cc-history-loading-dots" aria-hidden="true">···</span></span>
    </div>
  );
}
