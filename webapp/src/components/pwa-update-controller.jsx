import React, { useEffect, useState } from 'react';
import {
  getPwaUpdateServiceWorker,
  subscribeToPwaRefresh,
} from '../pwa-registration';
import './pwa-controller.css';

export default function PwaUpdateController() {
  const [needRefresh, setNeedRefresh] = useState(false);

  useEffect(() => subscribeToPwaRefresh(() => setNeedRefresh(true)), []);

  if (!needRefresh) return null;

  return (
    <aside className="cc-pwa-prompt cc-pwa-prompt--compact" aria-label="应用更新">
      <div className="cc-pwa-prompt-copy">
        <strong>发现新版本</strong>
        <span>更新后将重新加载应用。</span>
      </div>
      <div className="cc-pwa-prompt-actions">
        <button type="button" onClick={() => getPwaUpdateServiceWorker()?.(true)}>立即更新</button>
        <button type="button" className="secondary" onClick={() => setNeedRefresh(false)}>稍后</button>
      </div>
    </aside>
  );
}
