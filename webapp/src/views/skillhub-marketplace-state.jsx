import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { getAuthRevision, getToken } from '../auth-session';
import { marketplaceApi } from '../skillhub-marketplace-api';
import { normalizeSkillHubSkills } from '../utils/skillhub-entry';

export const MARKET_CATEGORIES = [
  ['research', '搜索与研究'], ['documents', '文档与写作'], ['data', '数据与表格'],
  ['media', '图像与影音'], ['web', '网页与应用'], ['development', '编程与开发'],
  ['automation', '办公与自动化'], ['other', '其他 / 待分类'],
];
const disabled = {
  enabled: false,
  writesEnabled: false,
  visibilityEnabled: false,
  visibilityWritesEnabled: false,
};
const Context = createContext(disabled);
export const useMarketplace = () => useContext(Context);

export function mergeMarketplaceLibrary(legacySkills, cloudSkills, category) {
  const local = category ? [] : legacySkills.filter((s) => s.isLocalSkill);
  const localIDs = new Set(local.flatMap((s) => [s.skillId, s.cloudSkillId].filter(Boolean)));
  return [...local, ...cloudSkills.filter((s) => !localIDs.has(s.skillId))];
}

export function MarketplaceProvider({ children }) {
  const [revision, setRevision] = useState(getAuthRevision);
  const [capabilities, setCapabilities] = useState(disabled);
  const leaveGuard = useRef(null);
  const registerLeaveGuard = useCallback((guard) => {
    leaveGuard.current = guard;
    return () => { if (leaveGuard.current === guard) leaveGuard.current = null; };
  }, []);
  const navigate = useCallback((action) => {
    if (!leaveGuard.current) { action(); return; }
    const guard = leaveGuard.current;
    Promise.resolve(guard()).then((allowed) => { if (allowed && leaveGuard.current === guard) action(); });
  }, []);
  const value = useMemo(() => ({ ...capabilities, registerLeaveGuard, navigate }), [capabilities, registerLeaveGuard, navigate]);
  useEffect(() => {
    const change = () => { setCapabilities(disabled); setRevision(getAuthRevision()); };
    window.addEventListener('cc:auth-changed', change);
    return () => window.removeEventListener('cc:auth-changed', change);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    if (getToken()) marketplaceApi.capabilities(controller.signal).then((value) => {
      if (!controller.signal.aborted) setCapabilities(value?.schemaVersion === 1 ? {
        enabled: value.enabled === true,
        writesEnabled: value.enabled === true && value.writesEnabled === true,
        visibilityEnabled: value.visibilityEnabled === true,
        visibilityWritesEnabled: value.visibilityEnabled === true && value.visibilityWritesEnabled === true,
      } : disabled);
    }).catch(() => {}); // Old server / feature off: original UI remains usable.
    return () => controller.abort();
  }, [revision]);
  return <Context.Provider value={value}><React.Fragment key={revision}>{children}</React.Fragment></Context.Provider>;
}

// The new filtered catalogue is display-only. It does not replace the legacy
// metadata map used by Agent status, update detection, or installation rules.
export function useMarketplaceCatalogue(query) {
  const { enabled } = useMarketplace();
  const [category, setCategory] = useState('');
  const [state, setState] = useState(null);
  const [reload, setReload] = useState(0);
  const pending = useRef(null);
  const key = JSON.stringify([query, category, reload]);
  const keyRef = useRef(key);
  keyRef.current = key;
  const load = async (cursor = '', previous = null) => {
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    setState({ key, ...(previous || {}), loading: true, error: '' });
    try {
      const result = await marketplaceApi.catalogue({ q: query, category, search_mode: 'name', limit: '30', ...(cursor ? { cursor } : {}) }, controller.signal);
      if (controller.signal.aborted || keyRef.current !== key) return;
      if (!Array.isArray(result?.skills) || !Number.isSafeInteger(result.total) || result.total < 0
        || (result.nextCursor != null && typeof result.nextCursor !== 'string')) throw new Error('能力库数据格式暂不支持');
      const entries = normalizeSkillHubSkills(result);
      setState({ key, loading: false, skills: [...new Map([...(previous?.skills || []), ...entries].map((s) => [s.skillId, s])).values()],
        total: result.total, cursor: result.nextCursor || '', counts: Object.fromEntries((Array.isArray(result.categoryCounts) ? result.categoryCounts : [])
          .filter((c) => typeof c?.category === 'string' && Number.isSafeInteger(c.count) && c.count >= 0)
          .map((c) => [c.category, c.count])) });
    } catch (error) {
      if (controller.signal.aborted || keyRef.current !== key) return;
      if (cursor && error.status === 409) {
        setState(null);
        setReload((value) => value + 1); // Restart page one, never retry a mutation.
      } else setState({ key, ...(previous || {}), loading: false, error: previous ? '后续页面暂不可用，已保留当前结果。' : '新版分类暂不可用，以下显示原有能力库（未按分类筛选）。' });
    }
  };
  useEffect(() => {
    if (!enabled) { setState(null); return undefined; }
    const timer = window.setTimeout(() => load(), 250);
    return () => { window.clearTimeout(timer); pending.current?.abort(); };
  // key encodes every filter and the explicit refresh generation.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, key]);
  return { enabled, category, setCategory, state: state?.key === key ? state : null,
    more: () => { if (state?.cursor && !state.loading) load(state.cursor, state); },
    refresh: () => setReload((value) => value + 1) };
}

export function MarketplaceFilters({ market }) {
  if (!market.enabled) return null;
  return <div className='cc-market-filters'>
    <div role='group' aria-label='按用途分类'>
      {[['', '全部'], ...MARKET_CATEGORIES].map(([id, label]) => <button type='button' key={id}
        aria-pressed={market.category === id} onClick={() => market.setCategory(id)}>{label}
        {id && Number.isSafeInteger(market.state?.counts?.[id]) && <span>{market.state.counts[id]}</span>}
      </button>)}
    </div>
    {market.state?.error && <p role='status'>{market.state.error}</p>}
    {market.state?.skills && <small>云端能力 {market.state.skills.length} / {market.state.total} 项{!market.category ? ' · 本机能力单独保留' : ''}</small>}
    <button type='button' disabled={market.state?.loading} onClick={market.refresh}>{market.state?.error ? '重试分类' : '刷新分类'}</button>
  </div>;
}
