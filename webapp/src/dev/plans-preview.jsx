import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter';
import '@fontsource-variable/noto-sans-sc';
import '../views/workspace-styles';
import { api } from '../api';
import { FeedbackProvider } from '../components/feedback-system';
import RelayAccessModal from '../widgets/relay-access-modal';
import EnterpriseReference from '../widgets/plans/enterprise-services';
import EnterpriseTeam from '../widgets/plans/enterprise-team';
import { personalPlanOffers as plans, planPresentationOverrides, tokenEstimateNote } from '../widgets/plans/plan-offers';
import '../widgets/plans/plan-chooser.css';
import './plans-preview.css';
import './plans-enterprise-variants.css';

// Draft monthly pricing for the three plans. This document
// uses no account or real orders; prices here do not update the live catalogue.

api.getRelayConfig = async () => ({ self_service_enabled: false });
api.getRelayCommercial = async () => ({ enabled: true, summary: { uid: 0, models: [], entitlements: [], plans } });
api.getCommercialCatalog = async () => ({ enabled: true, plans, channels: [{ id: 'preview', label: '预览' }], trial_available: false });
api.getCommercialOrders = async () => ({ orders: [] });
api.getRelayUsage = async () => ({ configured: false, summary: null });
// API writes stay local to this document; the enterprise contact link is preserved.
const previewOnly = async () => { throw new Error('当前为界面预览，不会创建订单或发起支付。'); };
for (const method of ['createCommercialOrder', 'cancelCommercialOrder', 'confirmCommercialTestPayment', 'claimCommercialTrial',
  'createRelaySession', 'createRelayKey', 'rotateRelayKey', 'revealRelayKey', 'revokeRelayKey', 'redeemRelayInvite']) {
  api[method] = previewOnly;
}

function PlansPreview() {
  const [theme, setTheme] = useState('light');
  const [open, setOpen] = useState(true);
  const [revision, setRevision] = useState(0);
  const [showChooser, setShowChooser] = useState(true);
  const [enterpriseLayout, setEnterpriseLayout] = useState(() => {
    const layout = new URLSearchParams(location.search).get('enterprise');
    return ['a', 'b', 'c', 'd', 'e'].includes(layout) ? layout : 'e';
  });
  const [enterpriseRequested, setEnterpriseRequested] = useState(() => new URLSearchParams(location.search).has('enterprise'));
  useEffect(() => { document.documentElement.dataset.enterpriseLayout = enterpriseLayout; }, [enterpriseLayout]);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.liquidVariant = 'green';
  }, [theme]);
  useEffect(() => {
    if (!open || !showChooser) return undefined;
    const openChooser = () => {
      const button = document.querySelector('button[aria-controls="relay-access-plan-chooser"]');
      if (!button || button.disabled) return;
      observer.disconnect();
      button.click();
    };
    const observer = new MutationObserver(openChooser);
    observer.observe(document.getElementById('root'), { childList: true, subtree: true });
    openChooser();
    return () => observer.disconnect();
  }, [open, revision, showChooser]);
  useEffect(() => {
    if (!enterpriseRequested || !open || !showChooser) return undefined;
    const selectEnterprise = () => {
      const tab = document.getElementById('relay-access-enterprise-plan-tab');
      if (!tab) return;
      observer.disconnect();
      tab.click();
    };
    const observer = new MutationObserver(selectEnterprise);
    observer.observe(document.getElementById('root'), { childList: true, subtree: true });
    selectEnterprise();
    return () => observer.disconnect();
  }, [enterpriseRequested, open, revision, showChooser]);
  const show = chooser => { setShowChooser(chooser); setOpen(true); setRevision(value => value + 1); };
  return <FeedbackProvider>
    <header className="plans-preview-toolbar">
      <strong>套餐界面预览</strong>
      <span>套餐调整草案 · 示例账号 · 不发起支付</span>
      <nav aria-label="预览控制">
        <button onClick={() => show(true)}>套餐选择</button>
        <button onClick={() => show(false)}>权益概览</button>
        <label>企业排版<select aria-label="企业排版" value={enterpriseLayout} onChange={event => {
          const layout = event.target.value;
          setEnterpriseLayout(layout);
          setEnterpriseRequested(true);
          const url = new URL(location.href);
          url.searchParams.set('enterprise', layout);
          history.replaceState(null, '', url);
          show(true);
        }}>
          <option value="a">A · 紧凑双栏</option><option value="b">B · 独立双卡</option>
          <option value="c">C · 开放清单</option><option value="d">D · 居中单栏</option>
          <option value="e">E · 图文分区</option>
        </select></label>
        <label>主题<select aria-label="预览主题" value={theme} onChange={event => setTheme(event.target.value)}>
          <option value="light">浅色</option><option value="dark">深色</option><option value="liquid">液态</option>
        </select></label>
      </nav>
    </header>
    <main className="plans-preview-empty"><h1>套餐与权益</h1><button onClick={() => show(true)}>打开套餐预览</button></main>
    {open && <RelayAccessModal key={revision} onClose={() => setOpen(false)} showFreePlan={false} planPresentationOverrides={planPresentationOverrides} planChooserNote={tokenEstimateNote} enterprisePlanContent={enterpriseLayout === 'e' ? ({ onSelectPlan }) => <><EnterpriseTeam onSelectPlan={onSelectPlan} /><EnterpriseReference /></> : null} />}
  </FeedbackProvider>;
}

createRoot(document.getElementById('root')).render(<PlansPreview />);
