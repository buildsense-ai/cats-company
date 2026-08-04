import React from 'react';
import { X } from 'lucide-react';
import { api } from '../api';

export default function RelayAdminModal({ onClose }) {
  return (
    <div className="v3-relay-admin-overlay" role="dialog" aria-label="中转用量管理">
      <div className="v3-relay-admin-modal">
        <div className="v3-relay-admin-header">
          <span>中转用量管理</span>
          <button type="button" onClick={onClose} aria-label="关闭中转用量管理" title="关闭">
            <X size={16} />
          </button>
        </div>
        {/* The relay page needs scripts and same-origin fetches (path-rewritten
            through the guarded proxy). allow-same-origin + allow-scripts means the
            sandbox is a thin layer only; the real boundary is the server-side
            proxy (uid whitelist + path whitelist + scoped cookie + rate limit),
            and only whitelisted uid can open this modal at all. */}
        <iframe
          title="中转用量管理"
          src={api.relayAdminProxyURL('/local/usage-admin')}
          sandbox="allow-scripts allow-same-origin allow-forms"
        />
      </div>
    </div>
  );
}
