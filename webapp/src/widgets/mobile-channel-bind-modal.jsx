import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Copy, RefreshCw, Unlink, X } from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import QRCode from './qr-code';

const MOBILE_CHANNELS = [
  { value: 'weixin', label: '公众号', displayName: '微信公众号' },
  { value: 'feishu', label: '飞书', displayName: '飞书' },
  { value: 'weixin_clawbot', label: 'ClawBot', displayName: '微信 ClawBot' },
];

const channelMeta = (value) => (
  MOBILE_CHANNELS.find((item) => item.value === value) || MOBILE_CHANNELS[0]
);

export default function MobileChannelBindModal({ agentUid, agentName, groupId, topicId, groupName, onClose }) {
  const feedback = useFeedback();
  const [channel, setChannel] = useState('weixin');
  const [linkInfo, setLinkInfo] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);
  const [clawBotAuthStatus, setClawBotAuthStatus] = useState(null);
  const [feishuBindings, setFeishuBindings] = useState([]);
  const [bindingsLoading, setBindingsLoading] = useState(false);
  const [bindingsError, setBindingsError] = useState('');
  const [unlinkingKey, setUnlinkingKey] = useState('');
  const [bindingNotice, setBindingNotice] = useState('');
  const requestSeqRef = useRef(0);
  const bindingsRequestSeqRef = useRef(0);
  const bindingNoticeTimerRef = useRef(null);
  const isGroupTarget = Boolean(groupId || topicId);
  const targetName = isGroupTarget ? (groupName || '群聊') : agentName;

  const loadLink = useCallback(async () => {
    if (!isGroupTarget && !agentUid) return;
    const requestSeq = requestSeqRef.current + 1;
    requestSeqRef.current = requestSeq;
    try {
      setLoading(true);
      setError('');
      setCopied(false);
      setClawBotAuthStatus(null);
      setLinkInfo(null);
      const res = isGroupTarget
        ? await api.createChannelGroupMobileLink(groupId, topicId, channel)
        : await api.createChannelIdentityMobileLink(agentUid, channel);
      if (requestSeqRef.current !== requestSeq) return;
      setLinkInfo(res);
    } catch (err) {
      if (requestSeqRef.current !== requestSeq) return;
      setLinkInfo(null);
      setError(err.message || '暂时无法生成移动端入口');
    } finally {
      if (requestSeqRef.current === requestSeq) {
        setLoading(false);
      }
    }
  }, [agentUid, channel, groupId, isGroupTarget, topicId]);

  useEffect(() => {
    loadLink();
  }, [loadLink]);

  const loadFeishuBindings = useCallback(async () => {
    if (channel !== 'feishu' || (!isGroupTarget && !agentUid)) {
      bindingsRequestSeqRef.current += 1;
      setFeishuBindings([]);
      setBindingsLoading(false);
      setBindingsError('');
      return;
    }
    const requestSeq = bindingsRequestSeqRef.current + 1;
    bindingsRequestSeqRef.current = requestSeq;
    try {
      setBindingsLoading(true);
      setBindingsError('');
      const res = await api.getChannelPrivateBindings({
        agentUid: isGroupTarget ? null : agentUid,
        groupId: isGroupTarget ? groupId : null,
        topicId: isGroupTarget ? topicId : null,
      });
      if (bindingsRequestSeqRef.current !== requestSeq) return;
      setFeishuBindings(Array.isArray(res?.bindings) ? res.bindings : []);
    } catch (err) {
      if (bindingsRequestSeqRef.current !== requestSeq) return;
      setFeishuBindings([]);
      setBindingsError(err.message || '暂时无法读取绑定状态');
    } finally {
      if (bindingsRequestSeqRef.current === requestSeq) setBindingsLoading(false);
    }
  }, [agentUid, channel, groupId, isGroupTarget, topicId]);

  useEffect(() => {
    setBindingNotice('');
    loadFeishuBindings();
  }, [loadFeishuBindings]);

  useEffect(() => () => {
    if (bindingNoticeTimerRef.current) window.clearTimeout(bindingNoticeTimerRef.current);
  }, []);

  const handleRefresh = () => {
    loadLink();
    if (channel === 'feishu') loadFeishuBindings();
  };

  const handleUnlink = async (binding) => {
    const targetLabel = isGroupTarget ? `群聊“${targetName}”` : `虚拟员工“${targetName}”`;
    const confirmed = await feedback.confirm({
      title: '解除飞书绑定？',
      message: `将解除“${binding.display_name}”与${targetLabel}的绑定。该账号的飞书私聊将停止同步；已建立的飞书群聊不受影响。`,
      confirmLabel: '解除绑定',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      bindingsRequestSeqRef.current += 1;
      setBindingsLoading(false);
      setUnlinkingKey(binding.binding_key);
      setBindingsError('');
      setBindingNotice('');
      await api.unlinkChannelPrivateBinding({
        bindingKey: binding.binding_key,
        agentUid: isGroupTarget ? null : agentUid,
        groupId: isGroupTarget ? groupId : null,
        topicId: isGroupTarget ? topicId : null,
        selectedAt: binding.selected_at,
      });
      setFeishuBindings((current) => current.filter((item) => item.binding_key !== binding.binding_key));
      setBindingNotice('已解除飞书私聊绑定');
      if (bindingNoticeTimerRef.current) window.clearTimeout(bindingNoticeTimerRef.current);
      bindingNoticeTimerRef.current = window.setTimeout(() => setBindingNotice(''), 1800);
    } catch (err) {
      setBindingsError(err.message || '解绑失败，请刷新后重试');
    } finally {
      setUnlinkingKey('');
    }
  };

  const qrKind = linkInfo?.qr_kind || linkInfo?.entry?.qr_kind || '';
  const activeChannel = channelMeta(channel);
  const isWeixinOfficialQR = channel === 'weixin' && qrKind === 'weixin_official_qr';
  const isFeishuNativeUnconfigured = channel === 'feishu' && qrKind === 'feishu_native_unconfigured';
  const isClawBotIlinkQR = channel === 'weixin_clawbot' && qrKind === 'weixin_clawbot_ilink_qr';
  const isClawBotUnavailable = channel === 'weixin_clawbot' && linkInfo && !isClawBotIlinkQR;
  const shouldSuppressQRCode = (channel === 'weixin' && !isWeixinOfficialQR) || isFeishuNativeUnconfigured || isClawBotUnavailable;
  const qrValue = shouldSuppressQRCode ? '' : (linkInfo?.qr_value || linkInfo?.channel_qr_url || '');
  const channelImageURL = isWeixinOfficialQR ? (linkInfo?.channel_qr_url || '') : '';
  const copyValue = qrValue || '';
  const clawBotQRCode = linkInfo?.entry?.clawbot_entry_status?.qrcode || '';
  const sceneKey = linkInfo?.scene_key || '';

  useEffect(() => {
    if (channel !== 'weixin_clawbot' || !sceneKey || !clawBotQRCode || !qrValue) return undefined;
    let cancelled = false;
    let timer = null;
    const poll = async () => {
      try {
        const res = await api.getWeixinClawBotQRCodeStatus(sceneKey, clawBotQRCode);
        if (cancelled) return;
        if (res?.token_saved) {
          setClawBotAuthStatus({ status: 'saved', target: res.target || 'agent' });
          return;
        }
        setClawBotAuthStatus({ status: res?.status || 'waiting' });
        timer = window.setTimeout(poll, 2000);
      } catch (err) {
        if (cancelled) return;
        setClawBotAuthStatus({ status: 'error', message: err.message || '授权状态检查失败' });
        timer = window.setTimeout(poll, 3000);
      }
    };
    poll();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [channel, sceneKey, clawBotQRCode, qrValue]);

  const channelCopy = (() => {
    if (channel === 'weixin' && linkInfo && !isWeixinOfficialQR) {
      return '微信公众号参数二维码尚未配置，暂时不能生成公众号移动端绑定二维码。';
    }
    if (isFeishuNativeUnconfigured) {
      return '飞书原生入口尚未配置，暂时不能生成飞书移动端二维码。';
    }
    if (isClawBotUnavailable) {
      return '微信 ClawBot 授权二维码暂时不可用，请稍后刷新重试。';
    }
    if (channel === 'weixin_clawbot') {
      return '扫码会进入微信 ClawBot 授权流程；它不会像公众号一样直接进入该机器人聊天框，之后请在微信里打开 ClawBot 对话继续使用。';
    }
    if (isGroupTarget) {
      return `扫码后会把你的${activeChannel.displayName}身份绑定到当前 CatsCo 账号，之后可直接在移动端进入这个群聊。`;
    }
    return `扫码后会把你的${activeChannel.displayName}身份绑定到当前 CatsCo 账号，之后可直接在移动端继续和这个虚拟员工对话。`;
  })();
  const emptyQrText = isFeishuNativeUnconfigured
    ? '飞书原生入口尚未配置，暂时不能生成飞书移动端二维码'
    : isClawBotUnavailable
      ? '微信 ClawBot 授权二维码暂时不可用'
    : channel === 'weixin' && linkInfo && !isWeixinOfficialQR
      ? '微信公众号参数二维码尚未配置'
      : '暂时没有可用二维码';

  const handleCopy = async () => {
    if (!copyValue || !navigator.clipboard) return;
    try {
      await navigator.clipboard.writeText(copyValue);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    } catch (err) {
      setError('复制失败，请手动复制链接');
    }
  };

  return (
    <div className="oc-modal-overlay">
      <div
        className="oc-modal mobile-channel-bind-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="mobile-channel-dialog-title"
        aria-describedby="mobile-channel-dialog-description"
      >
        <div className="mobile-channel-bind-header">
          <div>
            <div className="oc-modal-title" id="mobile-channel-dialog-title">移动端使用</div>
            <div className="mobile-channel-bind-subtitle" id="mobile-channel-dialog-description">{targetName}</div>
          </div>
          <button type="button" className="v3-action-btn" onClick={onClose} aria-label="关闭">
            <X size={16} />
          </button>
        </div>

        <div className="mobile-channel-tabs">
          {MOBILE_CHANNELS.map((item) => (
            <button key={item.value} type="button" className={channel === item.value ? 'active' : ''} onClick={() => setChannel(item.value)}>
              {item.label}
            </button>
          ))}
        </div>

        <p className="mobile-channel-copy">{channelCopy}</p>

        <div className="mobile-channel-qr-wrap">
          {loading && <div className="mobile-channel-placeholder">正在生成...</div>}
          {!loading && error && <InlineFeedback tone="error" className="mobile-channel-error">{error}</InlineFeedback>}
          {!loading && !error && channelImageURL && (
            <img className="mobile-channel-qr-img" src={channelImageURL} alt={`${activeChannel.displayName}移动端绑定二维码`} />
          )}
          {!loading && !error && !channelImageURL && qrValue && (
            <div className="mobile-channel-qr-box">
              <QRCode value={qrValue} size={210} />
            </div>
          )}
          {!loading && !error && !qrValue && (
            <div className="mobile-channel-placeholder">{emptyQrText}</div>
          )}
        </div>

        {!loading && !error && qrValue && channel !== 'weixin_clawbot' && (
          <p className="mobile-channel-expiry">二维码 10 分钟内有效，完成绑定后会自动失效。</p>
        )}

        {!loading && !error && channel === 'weixin_clawbot' && qrValue && (
          <p className="mobile-channel-expiry">
            {clawBotAuthStatus?.status === 'saved'
              ? 'ClawBot 授权已保存，服务端会持续接收这个 ClawBot 的消息。'
              : clawBotAuthStatus?.status === 'error'
                ? `正在重试授权状态检查：${clawBotAuthStatus.message}`
                : '扫码后请保持这个窗口打开，授权确认后服务端会自动保存 token。'}
          </p>
        )}

        <div className="mobile-channel-actions">
          <button type="button" className="oc-btn oc-btn-default" onClick={handleCopy} disabled={!copyValue}>
            <Copy size={14} /> {copied ? '已复制' : '复制链接'}
          </button>
          <button type="button" className="oc-btn oc-btn-default" onClick={handleRefresh} disabled={loading || bindingsLoading || Boolean(unlinkingKey)}>
            <RefreshCw size={14} /> 刷新
          </button>
        </div>

        {channel === 'feishu' && (bindingsLoading || bindingsError || bindingNotice || feishuBindings.length > 0) && (
          <section className="mobile-channel-bindings" aria-label="已绑定飞书用户">
            {feishuBindings.length > 0 && (
              <div className="mobile-channel-bindings-heading">
                <span>已绑定飞书用户</span>
                <span>{feishuBindings.length}</span>
              </div>
            )}
            {bindingsLoading && <div className="mobile-channel-bindings-status" role="status" aria-live="polite">正在读取绑定状态...</div>}
            {!bindingsLoading && bindingsError && (
              <div className="mobile-channel-bindings-status mobile-channel-bindings-status-error" role="alert">
                <span>{bindingsError}</span>
                <button type="button" onClick={loadFeishuBindings}>重试</button>
              </div>
            )}
            {!bindingsLoading && !bindingsError && bindingNotice && (
              <div className="mobile-channel-bindings-status mobile-channel-bindings-status-success" role="status" aria-live="polite">{bindingNotice}</div>
            )}
            {feishuBindings.length > 0 && (
              <div className="mobile-channel-bindings-list">
                {feishuBindings.map((binding) => (
                  <div className="mobile-channel-binding-row" key={binding.binding_key}>
                    {binding.avatar_url ? (
                      <img src={binding.avatar_url} alt="" className="mobile-channel-binding-avatar" />
                    ) : (
                      <span className="mobile-channel-binding-avatar mobile-channel-binding-avatar-fallback">
                        {(binding.display_name || '飞').slice(0, 1)}
                      </span>
                    )}
                    <div className="mobile-channel-binding-user">
                      <strong>{binding.display_name || '飞书用户'}</strong>
                      <span>飞书</span>
                    </div>
                    <button
                      type="button"
                      className="mobile-channel-unlink-btn"
                      onClick={() => handleUnlink(binding)}
                      disabled={bindingsLoading || Boolean(unlinkingKey)}
                      aria-label={`解除${binding.display_name || '飞书用户'}的飞书绑定`}
                    >
                      <Unlink size={14} />
                      {unlinkingKey === binding.binding_key ? '解绑中...' : '解绑'}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </section>
        )}
      </div>
    </div>
  );
}
