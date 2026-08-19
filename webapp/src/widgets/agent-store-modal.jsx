import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { api, getWebSocketURL } from '../api';
import t from '../i18n';
import {
  ArrowLeft,
  Bot,
  Check,
  CheckCircle,
  ChevronDown,
  Cloud,
  Code2,
  Copy,
  FileCheck2,
  Plus,
  Puzzle,
  QrCode,
  RefreshCw,
  Search,
  Settings2,
  Trash2,
  X,
  XCircle,
  Zap,
} from 'lucide-react';
import Avatar from './avatar';
import QRCode from './qr-code';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';
import {
  normalizeLocalSkillHubSkills,
  normalizeSkillHubSkills,
  resolveSkillHubEntry,
} from '../utils/skillhub-entry';
import CustomSelect from './custom-select';
import CloudWorkerPanel from './cloud-worker-panel';
import AgentSystemPromptCard from './agent-system-prompt-card';
import AgentCapabilityVisualization from './agent-capability-visualization';

const CREATE_MODES = {
  SELF_HOSTED: 'self_hosted',
  MANAGED: 'managed',
};

// Cloud worker creation failure → user-facing message, keyed by the backend
// error code. Concrete technical reasons (e.g. cloud quota) stay in server
// logs and are never surfaced to the UI.
const cloudWorkerCreateMessage = (e) => {
  switch (e?.data?.code) {
    case 'cloud_worker_not_enabled':
      return '云端部署尚未为你的账号开放';
    case 'cloud_worker_quota_exhausted':
      return '云端虚拟员工配额已用完';
    case 'cloud_worker_provisioning_unconfigured':
      return '云端供给服务暂不可用，请联系管理员';
    case 'cloud_worker_provision_failed':
      return '云端资源供给失败，请稍后重试或联系管理员';
    case 'cloud_worker_provision_failed_pending_cleanup':
      return '云端实例供给失败，可能有残留实例待清理，可在列表中删除';
    case 'cloud_worker_operation_busy':
      return '另一项云员工操作正在执行，请等待完成后再创建';
    case 'cloud_worker_invalid_username':
    case 'cloud_worker_create_failed':
    default:
      return '云端资源创建失败，请稍后重试或联系管理员';
  }
};

const cloudWorkerActionMessage = (e, actionLabel) => {
  const code = e?.data?.code;
  if (code === 'cloud_worker_operation_busy' || e?.status === 409) {
    return '另一项云员工操作正在执行，请等待完成后再试';
  }
  const unavailableMessages = {
    cloud_worker_update_unconfigured: '云端更新服务尚未配置，请联系管理员',
    cloud_worker_rollback_unconfigured: '云端回滚服务尚未配置，请联系管理员',
    cloud_worker_reset_unconfigured: '云端重置服务尚未配置，请联系管理员',
    cloud_worker_delete_unconfigured: '云端删除服务尚未配置，请联系管理员',
  };
  if (unavailableMessages[code]) return unavailableMessages[code];
  if (['NETWORK_ERROR', 'REQUEST_TIMEOUT'].includes(e?.code)) {
    return `${actionLabel}连接中断，操作可能仍在服务器执行。请先等待并刷新状态，不要重复提交`;
  }
  if ([502, 503, 504].includes(e?.status)) {
    return `${actionLabel}未完成，云端服务暂不可用。请刷新状态确认后再重试`;
  }
  return e?.message || `${actionLabel}失败，请稍后重试`;
};

const CHANNEL_AGENT_ACCESS_MODES = {
  APPROVAL_REQUIRED: 'approval_required',
  PUBLIC: 'public',
};

const BOT_VISIBILITY = {
  PUBLIC: 'public',
  PRIVATE: 'private',
};

const CHANNEL_OPTIONS = [
  { value: 'weixin', label: '微信公众号', shortLabel: '公众号' },
  { value: 'feishu', label: '飞书', shortLabel: '飞书' },
  { value: 'weixin_clawbot', label: '微信 ClawBot', shortLabel: 'ClawBot' },
];

const normalizeChannelAgentAccessMode = (value) => (
  value === CHANNEL_AGENT_ACCESS_MODES.PUBLIC
    ? CHANNEL_AGENT_ACCESS_MODES.PUBLIC
    : CHANNEL_AGENT_ACCESS_MODES.APPROVAL_REQUIRED
);

const normalizeChannel = (value) => {
  const normalized = String(value || '').trim().toLowerCase();
  const safe = normalized.replace(/[-\s]+/g, '_');
  if (['wechat', 'weixin_mp', 'wechat_mp', 'weixin_official', 'wechat_official', 'weixin_official_account', 'wechat_official_account'].includes(safe)) return 'weixin';
  if (['clawbot', 'weixinclawbot', 'wechatclawbot', 'weixin_clawbot', 'wechat_clawbot'].includes(safe)) return 'weixin_clawbot';
  if (safe === 'lark') return 'feishu';
  return safe;
};

const channelLabel = (value) => (
  CHANNEL_OPTIONS.find((item) => item.value === normalizeChannel(value))?.label
  || value
  || '渠道'
);

const isManagedChannel = (value) => ['feishu', 'weixin', 'weixin_clawbot'].includes(normalizeChannel(value));

const isWeixinOfficialChannel = (value) => normalizeChannel(value) === 'weixin';

const isWeixinClawBotChannel = (value) => normalizeChannel(value) === 'weixin_clawbot';

const isFeishuChannel = (value) => normalizeChannel(value) === 'feishu';

const initialForm = {
  display_name: '',
  role: 'code_review',
  description: '',
};

const ASSISTANT_ROLES = [
  {
    value: 'code_review',
    label: '代码审查',
    description: '侧重阅读代码、发现问题和整理修改建议。',
    skillQuery: 'code review',
    skillKeywords: ['code review', 'code-review', 'reviewer', 'lint', 'static analysis', 'code quality', '代码审查', '代码质量'],
  },
  {
    value: 'debugging',
    label: '问题排查',
    description: '侧重定位故障、分析原因和验证修复路径。',
    skillQuery: 'debugging',
    skillKeywords: ['debug', 'debugger', 'bug', 'error', 'diagnostic', 'troubleshoot', '排查', '调试', '故障'],
  },
  {
    value: 'writing',
    label: '写作',
    description: '侧重内容整理、改写润色和结构化表达。',
    skillQuery: 'writing',
    skillKeywords: ['write', 'writing', 'author', 'editor', 'document', 'pdf', '写作', '编辑', '文档'],
  },
  {
    value: 'research',
    label: '研究',
    description: '侧重收集资料、比较信息和形成研究结论。',
    skillQuery: 'research',
    skillKeywords: ['research', 'search', 'analysis', 'analyst', 'data', 'crawler', '研究', '检索', '资料', '分析'],
  },
  {
    value: 'general',
    label: '通用',
    description: '适合跨场景任务，可通过 Skill 补充专用能力。',
    skillQuery: '',
    skillKeywords: ['general', 'assistant', 'workflow', 'productivity', 'prompt', '通用', '效率'],
  },
];

function scoreSkillForRole(skill, role) {
  const identity = `${skill.skillId} ${skill.displayName}`.toLowerCase();
  const description = String(skill.description || '').toLowerCase();
  return role.skillKeywords.reduce((score, keyword) => {
    const normalized = keyword.toLowerCase();
    return score + (identity.includes(normalized) ? 4 : 0) + (description.includes(normalized) ? 1 : 0);
  }, 0);
}

const SKILL_RECOMMENDATION_MIN_SCORE = 4;

function rankSkillsForRole(skills, role) {
  return skills
    .map((skill, index) => ({ skill, index, score: scoreSkillForRole(skill, role) }))
    .filter((entry) => entry.score >= SKILL_RECOMMENDATION_MIN_SCORE)
    .sort((left, right) => right.score - left.score || left.index - right.index)
    .slice(0, 3)
    .map((entry) => entry.skill);
}

const hasExactSkillHash = (value) => /^[0-9a-f]{64}$/.test(String(value || ''));

async function resolveSkillBindingRef(skill) {
  let resolved = resolveSkillHubEntry(skill, skill);
  if (!resolved.latestVersion || !hasExactSkillHash(resolved.contentHash)) {
    const detail = await api.getSkillHubSkill(skill.skillId);
    resolved = resolveSkillHubEntry(skill, detail);
  }
  if (!resolved.latestVersion || !hasExactSkillHash(resolved.contentHash)) {
    throw new Error(`${resolved.displayName || resolved.skillId} 暂时没有可绑定的稳定版本。`);
  }
  return {
    source: 'skillhub',
    skillId: resolved.skillId,
    version: resolved.latestVersion,
    contentHash: resolved.contentHash,
  };
}

function sharedLocalSkillID(shared, fallback = '') {
  return String(
    shared?.skill?.id
    || shared?.skill?.skillId
    || shared?.skill?.skill_id
    || shared?.upload?.skillId
    || shared?.upload?.skill_id
    || shared?.submission?.normalizedManifest?.id
    || fallback,
  ).trim();
}

async function resolveSharedLocalSkill(skill, shared) {
  const skillId = sharedLocalSkillID(shared);
  if (!skillId) throw new Error('SkillHub 没有返回已同步 Skill 的标识。');

  const sharedVersion = String(
    shared?.latestVersion
    || shared?.latest_version
    || shared?.version
    || shared?.submission?.normalizedManifest?.version
    || '',
  ).trim();
  const sharedHash = String(
    shared?.contentHash
    || shared?.content_hash
    || shared?.upload?.contentHash
    || shared?.upload?.content_hash
    || shared?.submission?.contentHash
    || shared?.submission?.content_hash
    || '',
  ).trim().toLowerCase();
  let resolved = resolveSkillHubEntry({
    ...skill,
    skillId,
    latestVersion: sharedVersion,
    contentHash: sharedHash,
  }, shared);

  if (!resolved.latestVersion || !hasExactSkillHash(resolved.contentHash)) {
    const detail = await api.getSkillHubSkill(skillId, { fresh: true });
    resolved = resolveSkillHubEntry({ ...skill, skillId }, detail);
  }
  if (!resolved.latestVersion || !hasExactSkillHash(resolved.contentHash)) {
    throw new Error('Skill 已上传，但 SkillHub 尚未生成可绑定的稳定版本。');
  }

  return {
    ...skill,
    ...resolved,
    skillId,
    cloudSkillId: skillId,
    isLocalSkill: true,
    canBind: true,
  };
}

const isOwnedBot = (bot) => bot?.is_owner === true || bot?.relation === 'owner';

const mergeCloudWorkerFacts = (bots, workers) => {
  const byTenant = new Map(
    (workers || []).filter((worker) => worker?.tenant_name).map((worker) => [worker.tenant_name, worker]),
  );
  return bots.map((bot) => {
    const cloud = byTenant.get(bot.tenant_name);
    if (!cloud) return bot;
    const reportedStatus = String(cloud.cloud_status || cloud.status || '').toLowerCase();
    const presenceFallback = bot.is_online === true || bot.online === true
      ? 'online'
      : (bot.is_online === false || bot.online === false ? 'offline' : '');
    const cloudStatus = !reportedStatus || reportedStatus === 'unknown' || reportedStatus === 'unavailable'
      ? (presenceFallback || reportedStatus || 'unavailable')
      : reportedStatus;
    return {
      ...bot,
      cloud_status: cloudStatus,
      app_version: cloud.app_version || bot.app_version,
      cloud_version: cloud.cloud_version || cloud.version,
      cloud_image_id: cloud.cloud_image_id || cloud.image_id,
    };
  });
};

const normalizeAssistantRole = (value) => (
  ASSISTANT_ROLES.some((role) => role.value === value) ? value : 'general'
);

const editableBot = (bot) => ({
  ...bot,
  newDisplayName: bot.display_name,
  newAvatarUrl: bot.avatar_url || '',
  newRole: normalizeAssistantRole(bot.role),
  newDescription: String(bot.description || ''),
  newArtifactUploadEnabled: bot.artifact_upload_enabled !== false,
});

const normalizeBotVisibility = (visibility) => (
  visibility === BOT_VISIBILITY.PRIVATE ? BOT_VISIBILITY.PRIVATE : BOT_VISIBILITY.PUBLIC
);

const botVisibilityLabel = (visibility) => (
  normalizeBotVisibility(visibility) === BOT_VISIBILITY.PRIVATE ? '私有不可搜索' : '公开可搜索'
);

const botVisibilityDescription = (visibility) => (
  normalizeBotVisibility(visibility) === BOT_VISIBILITY.PRIVATE
    ? '不会出现在添加好友搜索里，也不能通过 UID 直接申请；已授权用户会保留，可在访问管理里移除。'
    : '别人可以通过名字或 UID 搜索并申请添加。'
);

function AgentManageSection({ id, title, summary, icon: Icon, open, onToggle, variant = '', children }) {
  const headingId = `${id}-heading`;
  const contentId = `${id}-content`;
  return (
    <section className={`cc-agent-manage-section${variant ? ` is-${variant}` : ''}${Icon ? '' : ' has-no-icon'}${open ? ' is-open' : ''}`}>
      <h3 id={headingId}>
        <button
          type="button"
          className="cc-agent-manage-section-trigger"
          aria-expanded={open}
          aria-controls={contentId}
          onClick={onToggle}
        >
          {Icon && <span className="cc-agent-manage-section-icon" aria-hidden="true"><Icon size={17} /></span>}
          <span className="cc-agent-manage-section-copy">
            <strong>{title}</strong>
            <small>{summary}</small>
          </span>
          <ChevronDown className="cc-agent-manage-section-chevron" size={17} aria-hidden="true" />
        </button>
      </h3>
      {open && (
        <div
          id={contentId}
          className="cc-agent-manage-section-body"
          role="region"
          aria-labelledby={headingId}
        >
          {children}
        </div>
      )}
    </section>
  );
}

export default function AgentStoreModal({
  initialAgentId = null,
  onClose,
  onOpenSkillHub,
  onOpenCloudArtifacts,
  user,
  onBotsChanged,
}) {
  const feedback = useFeedback();
  const [bots, setBots] = useState([]);
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState('hub'); // 'hub', 'create', 'manage'
  const [hubCloudView, setHubCloudView] = useState(false); // hub tab: show cloud manage panel instead of the roster
  const [createForm, setCreateForm] = useState(initialForm);
  const [selectedSkills, setSelectedSkills] = useState([]);
  const [skillPickerOpen, setSkillPickerOpen] = useState(false);
  const [skillQuery, setSkillQuery] = useState('');
  const [skillCatalogue, setSkillCatalogue] = useState([]);
  const [skillCatalogueLoading, setSkillCatalogueLoading] = useState(false);
  const [skillCatalogueError, setSkillCatalogueError] = useState('');
  const [localSkills, setLocalSkills] = useState([]);
  const [localSkillsLoading, setLocalSkillsLoading] = useState(false);
  const [localSkillsError, setLocalSkillsError] = useState('');
  const [skillRecommendationCandidates, setSkillRecommendationCandidates] = useState([]);
  const [skillRecommendationLoading, setSkillRecommendationLoading] = useState(false);
  const [skillRecommendationError, setSkillRecommendationError] = useState('');
  const [skillDetail, setSkillDetail] = useState(null);
  const [skillSyncingID, setSkillSyncingID] = useState('');
  const [skillSyncError, setSkillSyncError] = useState('');
  const [skillSyncNotice, setSkillSyncNotice] = useState('');
  const [skillPanelTab, setSkillPanelTab] = useState('available');
  const [createMode, setCreateMode] = useState(CREATE_MODES.SELF_HOSTED);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [createdBot, setCreatedBot] = useState(null);
  const [createdProfile, setCreatedProfile] = useState(null);
  const [createdSkillWarning, setCreatedSkillWarning] = useState('');
  const [createdMode, setCreatedMode] = useState(CREATE_MODES.SELF_HOSTED);
  const [copiedField, setCopiedField] = useState('');
  const [copyingBotKey, setCopyingBotKey] = useState(null);
  const [cloudQuota, setCloudQuota] = useState(null); // {enabled,total,used,remaining}
  const [cloudQuotaError, setCloudQuotaError] = useState(false); // true when the quota fetch itself failed
  const [cloudImages, setCloudImages] = useState([]); // available worker image versions from the control plane meta
  const [cloudReleases, setCloudReleases] = useState([]); // published application releases for update/rollback
  const [cloudActions, setCloudActions] = useState(null); // configured cloud operation capabilities
  const [cloudActioning, setCloudActioning] = useState(null); // { name, action }
  const [editingBot, setEditingBot] = useState(null);
  const [manageSection, setManageSection] = useState('basic');
  const [managedSkills, setManagedSkills] = useState({ count: 0, skills: [], loading: false, error: '' });
  const [artifactSummary, setArtifactSummary] = useState({
    count: 0,
    uploaderCount: 0,
    loading: false,
    error: '',
  });
  const [entryBot, setEntryBot] = useState(null);
  const avatarFileRef = useRef(null);
  const dialogRef = useRef(null);
  const dialogOpenerRef = useRef(null);
  const skillPickerRef = useRef(null);
  const skillPickerSearchRef = useRef(null);
  const skillPickerOpenerRef = useRef(null);
  const skillCatalogueRequestRef = useRef(0);
  const skillDetailDialogRef = useRef(null);
  const skillDetailCloseRef = useRef(null);
  const skillDetailOpenerRef = useRef(null);
  const skillDetailRequestRef = useRef(0);
  const editingBotRef = useRef(null);
  const [avatarUploading, setAvatarUploading] = useState(false);
  const initialAgentAppliedRef = useRef(false);
  const botOverview = useMemo(() => {
    const online = bots.filter((bot) => bot.is_online === true || bot.online === true).length;
    const publiclySearchable = bots.filter(
      (bot) => normalizeBotVisibility(bot.visibility) === BOT_VISIBILITY.PUBLIC,
    ).length;
    const managed = bots.filter((bot) => Boolean(bot.tenant_name)).length;
    return {
      total: bots.length,
      online,
      publiclySearchable,
      selfHosted: bots.length - managed,
    };
  }, [bots]);
  const selectedRole = useMemo(
    () => ASSISTANT_ROLES.find((role) => role.value === createForm.role) || ASSISTANT_ROLES[0],
    [createForm.role],
  );
  const installedSkills = useMemo(() => {
    const byID = new Map();
    localSkills.forEach((skill) => byID.set(skill.skillId, skill));
    selectedSkills.forEach((skill) => {
      const installed = byID.get(skill.skillId);
      byID.set(skill.skillId, installed ? { ...installed, ...skill, isLocalSkill: true } : skill);
    });
    return Array.from(byID.values());
  }, [localSkills, selectedSkills]);
  const availableSkills = useMemo(() => {
    const byID = new Map();
    skillRecommendationCandidates.slice(0, 3).forEach((skill) => {
      byID.set(skill.skillId, { ...skill, isRecommended: true });
    });
    installedSkills.forEach((skill) => {
      const recommendation = byID.get(skill.skillId);
      byID.set(skill.skillId, recommendation
        ? { ...recommendation, ...skill, isRecommended: true }
        : { ...skill, isRecommended: false });
    });
    return Array.from(byID.values());
  }, [installedSkills, skillRecommendationCandidates]);

  const loadSkillCatalogue = useCallback(async (query) => {
    const requestID = skillCatalogueRequestRef.current + 1;
    skillCatalogueRequestRef.current = requestID;
    setSkillCatalogueLoading(true);
    setSkillCatalogueError('');
    try {
      const response = await api.searchSkillHubSkills(String(query || '').trim());
      if (requestID !== skillCatalogueRequestRef.current) return;
      setSkillCatalogue(normalizeSkillHubSkills(response));
    } catch (loadError) {
      if (requestID !== skillCatalogueRequestRef.current) return;
      setSkillCatalogue([]);
      setSkillCatalogueError(loadError?.message || '暂时无法读取 SkillHub，请稍后重试。');
    } finally {
      if (requestID === skillCatalogueRequestRef.current) setSkillCatalogueLoading(false);
    }
  }, []);

  const openSkillPicker = () => {
    skillPickerOpenerRef.current = document.activeElement;
    setSkillPickerOpen(true);
  };

  const openCreateTab = () => {
    setSkillPanelTab('available');
    setTab('create');
  };

  const closeSkillPicker = useCallback(() => setSkillPickerOpen(false), []);

  const toggleSelectedSkill = (skill) => {
    setSelectedSkills((current) => (
      current.some((item) => item.skillId === skill.skillId)
        ? current.filter((item) => item.skillId !== skill.skillId)
        : [...current, skill]
    ));
  };

  const handleSkillPanelKeyDown = (event) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const nextTab = event.key === 'ArrowRight' || event.key === 'End'
      ? 'available'
      : 'selected';
    setSkillPanelTab(nextTab);
    event.currentTarget.parentElement
      ?.querySelector(`[data-skill-panel-tab="${nextTab}"]`)
      ?.focus();
  };

  const closeSkillDetails = useCallback(() => {
    skillDetailRequestRef.current += 1;
    setSkillSyncError('');
    setSkillDetail(null);
  }, []);

  const openSkillDetails = async (skill) => {
    const requestID = skillDetailRequestRef.current + 1;
    skillDetailRequestRef.current = requestID;
    skillDetailOpenerRef.current = document.activeElement;
    if (skill.isLocalSkill && !skill.cloudSkillId) {
      setSkillDetail({ skill, details: skill, loading: false, error: '' });
      return;
    }
    setSkillDetail({ skill, details: skill, loading: true, error: '' });
    try {
      const response = await api.getSkillHubSkill(skill.skillId);
      if (requestID !== skillDetailRequestRef.current) return;
      setSkillDetail({
        skill,
        details: resolveSkillHubEntry(skill, response),
        loading: false,
        error: '',
      });
    } catch (detailError) {
      if (requestID !== skillDetailRequestRef.current) return;
      setSkillDetail({
        skill,
        details: skill,
        loading: false,
        error: detailError?.message || '暂时无法读取完整参数。',
      });
    }
  };

  const syncAndSelectLocalSkill = async (skill) => {
    if (!skill?.isLocalSkill || skill.canBind !== false || skillSyncingID) return;
    const localName = skill.localSkillId || skill.displayName;
    setSkillSyncingID(skill.skillId);
    setSkillSyncError('');
    setSkillSyncNotice('');
    try {
      let shared = await api.shareLocalSkill(localName, '', user?.uid);
      if (shared?.requiresConfirmation || shared?.requires_confirmation) {
        const confirmed = globalThis.confirm?.(
          `SkillHub 已存在“${skill.displayName}”。是否将当前本地内容发布为新版本？`,
        );
        if (!confirmed) return;
        shared = await api.shareLocalSkill(localName, '', user?.uid, { confirmPublish: true });
        if (shared?.requiresConfirmation || shared?.requires_confirmation) {
          throw new Error('SkillHub 未接受新版本发布确认，请稍后重试。');
        }
      }
      const syncedSkill = await resolveSharedLocalSkill(skill, shared);
      setLocalSkills((current) => current.map((item) => (
        item.skillId === skill.skillId || (
          skill.localSkillId && item.localSkillId === skill.localSkillId
        )
          ? syncedSkill
          : item
      )));
      setSelectedSkills((current) => [
        ...current.filter((item) => (
          item.skillId !== skill.skillId
          && item.skillId !== syncedSkill.skillId
          && (!skill.localSkillId || item.localSkillId !== skill.localSkillId)
        )),
        syncedSkill,
      ]);
      setSkillSyncNotice(`“${skill.displayName}”已同步并添加。`);
      setSkillPanelTab('selected');
      closeSkillDetails();
    } catch (syncError) {
      setSkillSyncError(
        `${syncError?.message || '同步失败'} 当前 Skill 尚未添加到助手，可以稍后重试。`,
      );
    } finally {
      setSkillSyncingID('');
    }
  };

  // Cloud-managed workers shown in the dedicated cloud panel (create tab).
  const cloudWorkers = useMemo(
    () => bots.filter((bot) => Boolean(bot.tenant_name)),
    [bots],
  );

  useEffect(() => {
    initialAgentAppliedRef.current = false;
    loadBots();
  }, [initialAgentId]);

  // Application releases and base images are independent catalogs. A cold
  // backend snapshot gets one short follow-up poll; settled responses do not
  // keep polling.
  useEffect(() => {
    let cancelled = false;
    let retryTimer = null;
    const loadMeta = async () => {
      try {
        const meta = await api.getCloudWorkerMeta?.();
        if (cancelled) return;
        setCloudImages(meta?.images || []);
        setCloudReleases(meta?.releases || []);
        setCloudActions(meta?.actions || null);
        if (meta?.images_refreshing || meta?.releases_refreshing) {
          retryTimer = window.setTimeout(loadMeta, 2_000);
        }
      } catch {
        // Cloud image metadata is optional; worker management remains usable.
      }
    };
    loadMeta();
    return () => {
      cancelled = true;
      if (retryTimer) window.clearTimeout(retryTimer);
    };
  }, []);

  useEffect(() => {
    editingBotRef.current = editingBot;
  }, [editingBot]);

  useEffect(() => {
    const botId = editingBot?.id || editingBot?.uid;
    let active = true;
    if (!botId || tab !== 'manage') {
      setManagedSkills({ count: 0, skills: [], loading: false, error: '' });
      return undefined;
    }
    setManagedSkills((current) => ({ ...current, loading: true, error: '' }));
    api.getBotDefinitionSkills(botId)
      .then((response) => {
        if (!active) return;
        const skills = Array.isArray(response?.skills) ? response.skills : [];
        setManagedSkills({
          count: skills.length,
          skills,
          loading: false,
          error: '',
        });
      })
      .catch(() => {
        if (!active) return;
        setManagedSkills({ count: 0, skills: [], loading: false, error: '暂时无法读取能力数量' });
      });
    return () => {
      active = false;
    };
  }, [editingBot?.id, editingBot?.uid, tab]);

  useEffect(() => {
    const botId = editingBot?.id || editingBot?.uid;
    let active = true;
    if (!botId || tab !== 'manage' || manageSection !== 'collaboration') {
      return undefined;
    }
    setArtifactSummary((current) => ({ ...current, loading: true, error: '' }));
    api.getCloudArtifacts(botId, 'active')
      .then((response) => {
        if (!active) return;
        const artifacts = Array.isArray(response?.artifacts) ? response.artifacts : [];
        const uploaders = new Set(
          artifacts
            .filter((artifact) => artifact?.creator_type === 'user' || artifact?.uploader_uid || artifact?.uploader_name)
            .map((artifact) => String(
              artifact.uploader_uid
              || artifact.creator_uid
              || artifact.uploader_name
              || artifact.creator_name
              || '',
            ))
            .filter(Boolean),
        );
        setArtifactSummary({
          count: artifacts.length,
          uploaderCount: uploaders.size,
          loading: false,
          error: '',
        });
      })
      .catch(() => {
        if (!active) return;
        setArtifactSummary({ count: 0, uploaderCount: 0, loading: false, error: '暂时无法读取成果统计' });
      });
    return () => {
      active = false;
    };
  }, [editingBot?.id, editingBot?.uid, manageSection, tab]);

  useEffect(() => {
    if (tab === 'manage' && editingBot) {
      setManageSection('basic');
    }
  }, [editingBot?.id, editingBot?.uid, editingBot?.tenant_name, tab]);

  useEffect(() => {
    if (tab !== 'create') return undefined;
    let active = true;
    setSkillRecommendationLoading(true);
    setSkillRecommendationCandidates([]);
    setSkillRecommendationError('');

    const loadRecommendation = async () => {
      try {
        const directResponse = await api.searchSkillHubSkills(selectedRole.skillQuery);
        if (!active) return;
        let ranked = rankSkillsForRole(normalizeSkillHubSkills(directResponse), selectedRole);
        if (ranked.length === 0 && selectedRole.skillQuery) {
          const fallbackResponse = await api.searchSkillHubSkills('');
          if (!active) return;
          ranked = rankSkillsForRole(normalizeSkillHubSkills(fallbackResponse), selectedRole);
        }
        if (active) setSkillRecommendationCandidates(ranked);
      } catch {
        if (active) {
          setSkillRecommendationCandidates([]);
          setSkillRecommendationError('推荐服务暂时不可用，已安装的 Skill 仍可正常添加。');
        }
      } finally {
        if (active) setSkillRecommendationLoading(false);
      }
    };

    loadRecommendation();
    return () => {
      active = false;
    };
  }, [selectedRole, tab]);

  useEffect(() => {
    if (tab !== 'create') return undefined;
    let active = true;
    setLocalSkillsLoading(true);
    setLocalSkillsError('');
    const loadLocalSkills = async () => {
      try {
        const response = await api.getLocalSkills();
        if (active) setLocalSkills(normalizeLocalSkillHubSkills(response));
      } catch (loadError) {
        if (!active) return;
        setLocalSkills([]);
        setLocalSkillsError(loadError?.message || '未连接本地 Skill 服务。');
      } finally {
        if (active) setLocalSkillsLoading(false);
      }
    };
    loadLocalSkills();
    return () => {
      active = false;
    };
  }, [tab]);

  useEffect(() => {
    if (!skillPickerOpen) return undefined;
    loadSkillCatalogue(skillQuery);
    const frame = window.requestAnimationFrame(() => skillPickerSearchRef.current?.focus());
    const handlePickerKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeSkillPicker();
        return;
      }
      if (event.key !== 'Tab' || !skillPickerRef.current) return;
      const focusable = Array.from(skillPickerRef.current.querySelectorAll(
        'button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => !element.hidden && element.getAttribute('aria-hidden') !== 'true');
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handlePickerKeyDown);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener('keydown', handlePickerKeyDown);
      if (skillPickerOpenerRef.current instanceof HTMLElement) {
        skillPickerOpenerRef.current.focus();
      }
    };
  }, [closeSkillPicker, loadSkillCatalogue, skillPickerOpen]);

  useEffect(() => {
    if (!skillDetail) return undefined;
    const frame = window.requestAnimationFrame(() => skillDetailCloseRef.current?.focus());
    const handleDetailKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeSkillDetails();
        return;
      }
      if (event.key !== 'Tab' || !skillDetailDialogRef.current) return;
      const focusable = Array.from(skillDetailDialogRef.current.querySelectorAll(
        'button:not(:disabled), [href], [tabindex]:not([tabindex="-1"])',
      )).filter((element) => !element.hidden && element.getAttribute('aria-hidden') !== 'true');
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handleDetailKeyDown);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener('keydown', handleDetailKeyDown);
      if (skillDetailOpenerRef.current instanceof HTMLElement) {
        skillDetailOpenerRef.current.focus();
      }
    };
  }, [Boolean(skillDetail), closeSkillDetails]);

  useEffect(() => {
    dialogOpenerRef.current = document.activeElement;
    const frame = window.requestAnimationFrame(() => {
      dialogRef.current?.querySelector('button:not(:disabled)')?.focus();
    });
    return () => {
      window.cancelAnimationFrame(frame);
      if (dialogOpenerRef.current instanceof HTMLElement) dialogOpenerRef.current.focus();
    };
  }, []);

  useEffect(() => {
    if (entryBot || skillPickerOpen || skillDetail) return undefined;
    const handleDialogKeyDown = (event) => {
      if (
        document.querySelector('.cc-agent-prompt-editor-overlay')
        || (event.target instanceof Element && event.target.closest('.cc-agent-prompt-editor-dialog'))
      ) {
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || !dialogRef.current) return;
      if (
        event.target instanceof Element
        && event.target.closest('.v3-custom-model-select-options.is-portal')
      ) {
        return;
      }
      const focusable = Array.from(dialogRef.current.querySelectorAll(
        'button:not(:disabled), input:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => !element.hidden && element.getAttribute('aria-hidden') !== 'true');
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      } else if (!dialogRef.current.contains(document.activeElement)) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handleDialogKeyDown);
    return () => document.removeEventListener('keydown', handleDialogKeyDown);
  }, [entryBot, onClose, skillDetail, skillPickerOpen]);

  const loadBots = async ({ silent = false } = {}) => {
    const cloudRequest = api.getCloudWorkers
      ? api.getCloudWorkers().then((data) => ({ data })).catch((requestError) => ({ requestError }))
      : Promise.resolve({ data: {} });
    let manageableBots = null;
    try {
      if (!silent) setLoading(true);
      const [botsRes, agentsRes, friendsRes] = await Promise.all([
        api.getMyBots().catch((err) => {
          throw err;
        }),
        api.getAgents ? api.getAgents().catch(() => ({})) : Promise.resolve({}),
        api.getFriends ? api.getFriends().catch(() => ({})) : Promise.resolve({}),
      ]);
      manageableBots = mergeManageableBots(
        botsRes.bots || [],
        agentsRes.agents || [],
        friendsRes.friends || [],
      ).filter(isOwnedBot);
      // Core assistant data is entirely local to CatsCompany and should render
      // without waiting for cloud-provider reconciliation.
      setBots(manageableBots);

      if (
        !initialAgentAppliedRef.current
        && initialAgentId !== null
        && initialAgentId !== undefined
      ) {
        initialAgentAppliedRef.current = true;
        const requestedAgentId = String(initialAgentId);
        const requestedBot = manageableBots.find(
          (bot) => String(bot.id || bot.uid) === requestedAgentId,
        );
        if (requestedBot) {
          setEditingBot(editableBot(requestedBot));
          setTab('manage');
        }
      }
    } catch (e) {
      console.error('Load bots error:', e);
    } finally {
      if (!silent) setLoading(false);
    }

    if (!manageableBots) return;
    const { data: cloudRes = {}, requestError } = await cloudRequest;
    if (requestError) {
      setCloudQuotaError(true);
      return;
    }
    // Distinguish "quota fetch failed" (null + error) from "cloud hosting
    // disabled" (quota.enabled === false) so the UI does not mislead.
    setCloudQuotaError(!cloudRes.quota && !cloudRes.workers);
    setCloudQuota(cloudRes.quota || null);
    // Enrich provider-only facts after the roster is already visible. Online
    // presence and the application version continue to come from CatsCompany.
    setBots((current) => mergeCloudWorkerFacts(current, cloudRes.workers || []));
  };

  // Cloud status is operational data, not static bot metadata. Refresh only
  // while the managed panel is visible so a transient first-request failure
  // settles automatically without polling the rest of the assistant UI.
  useEffect(() => {
    const visible = (tab === 'hub' && hubCloudView)
      || (tab === 'create' && createMode === CREATE_MODES.MANAGED);
    if (!visible || !api.getCloudWorkers) return undefined;

    let active = true;
    let retryTimer = null;
    const refresh = async () => {
      try {
        const cloudRes = await api.getCloudWorkers();
        if (!active) return;
        setCloudQuotaError(false);
        setCloudQuota(cloudRes?.quota || null);
        setBots((current) => mergeCloudWorkerFacts(current, cloudRes?.workers || []));
        if (cloudRes?.status_refreshing) {
          if (retryTimer) window.clearTimeout(retryTimer);
          retryTimer = window.setTimeout(refresh, 2_000);
        }
      } catch {
        if (active) setCloudQuotaError(true);
      }
    };

    refresh();
    const timer = window.setInterval(refresh, 15_000);
    return () => {
      active = false;
      if (retryTimer) window.clearTimeout(retryTimer);
      window.clearInterval(timer);
    };
  }, [createMode, hubCloudView, tab]);

  const handleCreate = async (e) => {
    e.preventDefault();
    const displayName = createForm.display_name.trim();
    if (!displayName) {
      setError(t('bot_create_name_required'));
      return;
    }

    const slug = displayName.trim().toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').slice(0, 16);
    const suffix = Math.floor(Math.random() * 9000) + 1000;
    const username = `bot-${slug || 'bot'}-${suffix}`;
    const isManaged = createMode === CREATE_MODES.MANAGED;

    try {
      setError('');
      setCreatedBot(null);
      setCreatedProfile(null);
      setCreatedSkillWarning('');
      setIsSubmitting(true);

      const skillRefs = await Promise.all(selectedSkills.map(resolveSkillBindingRef));
      // Cloud-managed workers go through the cloud control plane (quota-checked,
      // provisions a Tianyi cloud instance). Self-hosted bots use the normal path.
      const result = isManaged
        ? await api.createCloudWorker({
            username,
            display_name: displayName,
            role: createForm.role,
            description: createForm.description.trim(),
          })
        : await api.createBot({
            username,
            display_name: displayName,
            role: createForm.role,
            description: createForm.description.trim(),
          });
      const fullResult = {
        ...result,
        id: result.uid,
        display_name: displayName,
        role: createForm.role,
        description: createForm.description.trim(),
        visibility: 'public',
      };

      // [CRITICAL HANDSHAKE]: Automatically force a bidirectional subscription so the bot 
      // instantly appears in both sides' Contact lists, avoiding ghost P2P topics.
      if (!isManaged && fullResult.api_key && user?.uid) {
        try {
          await api.sendFriendRequest(fullResult.uid);
          await api.acceptFriendAsBot(fullResult.api_key, user.uid);
          console.log('[Agent Handshake] Instantly bound P2P topic for developer testing.');
        } catch (handshakeErr) {
          console.warn('[Agent Handshake Failed]:', handshakeErr);
        }
      }

      if (skillRefs.length > 0) {
        try {
          const definition = await api.getBotDefinitionSkills(fullResult.uid);
          await api.updateBotDefinitionSkills(
            fullResult.uid,
            Number(definition?.revision || 0),
            skillRefs,
          );
        } catch (skillError) {
          console.warn('[Agent Skill Binding Failed]:', skillError);
          setCreatedSkillWarning(
            `助手已创建，但 Skill 未全部添加：${skillError?.message || '请稍后在 SkillHub 中重试。'}`,
          );
        }
      }

      setCreatedBot(fullResult);
      setCreatedProfile({
        role: { ...selectedRole },
        skills: selectedSkills.map((skill) => ({ ...skill })),
      });
      setCreatedMode(createMode);
      setTab('success');

      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
    } catch (e) {
      setError(e.message || t('error_server'));
    } finally {
      setIsSubmitting(false);
    }
  };

  // Cloud-managed creation from the dedicated cloud panel (create tab, managed mode).
  // Stays on the panel and refreshes the worker list instead of jumping to the
  // self-hosted API-key success screen.
  const handleCloudCreate = async (displayName) => {
    const trimmed = displayName.trim();
    if (!trimmed) return;
    const slug = trimmed.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').slice(0, 16);
    const suffix = Math.floor(Math.random() * 9000) + 1000;
    const username = `bot-${slug || 'bot'}-${suffix}`;
    try {
      setError('');
      await api.createCloudWorker({ username, display_name: trimmed });
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      feedback.notify({ tone: 'success', message: '云托管员工创建成功，云端实例供给中…' });
    } catch (e) {
      // 按后端错误码区分提示；具体技术原因（如云资源配额）只进后端日志
      const message = cloudWorkerCreateMessage(e);
      setError(message);
      throw new Error(message);
    }
  };

  const handleCopy = async (field, value) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopiedField(field);
      setTimeout(() => setCopiedField(''), 2000);
    } catch (e) {
      console.error('Copy failed:', e);
    }
  };

  const handleCopyBotAPIKey = async (bot, field = 'api_edit') => {
    const botId = bot?.id || bot?.uid;
    if (!botId) return;

    try {
      setError('');
      setCopyingBotKey(botId);

      let apiKey = bot.api_key;
      if (!apiKey) {
        const result = await api.getBotAPIKey(botId);
        apiKey = result.api_key;
      }
      if (!apiKey) throw new Error('API Key not found');

      setBots(prev => prev.map(item => item.id === botId ? { ...item, api_key: apiKey } : item));
      setEditingBot(prev => prev && (prev.id === botId || prev.uid === botId) ? { ...prev, api_key: apiKey } : prev);
      await handleCopy(field, apiKey);
    } catch (e) {
      setError(e.message || 'Failed to copy API Key');
    } finally {
      setCopyingBotKey(null);
    }
  };

  const handleDelete = async (bot) => {
    const botId = bot?.id || bot?.uid;
    if (!botId) return;
    const owned = isOwnedBot(bot);
    const confirmed = await feedback.confirm({
      title: owned ? `永久删除“${bot.display_name}”？` : `移除“${bot.display_name}”？`,
      message: owned
        ? '该虚拟员工及其相关配置会被永久删除，且无法恢复。'
        : '这只会解除好友关系，不会删除对方创建的虚拟员工。',
      confirmLabel: owned ? '永久删除' : '移除',
      tone: 'danger',
    });
    if (!confirmed) return;
    const cloudName = owned ? bot.tenant_name : '';
    try {
      if (cloudName) setCloudActioning({ name: cloudName, action: 'delete' });
      if (owned) {
        if (bot.tenant_name) {
          // Cloud workers are removed through the control plane so the cloud
          // instance gets destroyed (when a destroy script is configured).
          const result = await api.deleteCloudWorker(bot.tenant_name);
          if (result && result.warning) {
            feedback.notify({ tone: 'warning', message: result.warning });
          }
        } else {
          await api.deleteBot(botId);
        }
      } else {
        await api.removeFriend(botId);
      }
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      setTab('hub');
      feedback.notify({ tone: 'success', message: owned ? '虚拟员工已删除' : '助手已移除' });
    } catch (e) {
      setError(cloudName
        ? cloudWorkerActionMessage(e, '删除')
        : (e.message || t('error_server')));
    } finally {
      if (cloudName) setCloudActioning(null);
    }
  };

  const handleCloudUpdate = async (bot, version = '') => {
    const name = bot.tenant_name;
    if (!name) return;
    if (!version) {
      setError('暂无可用的应用发布版本，请稍后刷新后重试');
      return;
    }
    const confirmed = await feedback.confirm({
      title: `更新“${bot.display_name}”？`,
      message: `将应用更新到 ${version}，会保留会话、文件和本地配置。更新期间员工会短暂重启。`,
      confirmLabel: '确认更新',
      tone: 'default',
    });
    if (!confirmed) return;
    try {
      setCloudActioning({ name, action: 'update' });
      await api.updateCloudWorker(name, { version });
      await loadBots({ silent: true });
      feedback.notify({ tone: 'success', message: '应用更新完成' });
    } catch (e) {
      setError(cloudWorkerActionMessage(e, '更新'));
      await loadBots({ silent: true }).catch(() => {});
    } finally {
      setCloudActioning(null);
    }
  };

  // Cloud-managed rollback. The cloud panel passes an explicit application
  // release; the legacy caller can still fetch and choose one on demand.
  const handleCloudRollback = async (bot, version = '', opts = {}) => {
    const name = bot.tenant_name;
    if (!name) return;
    try {
      if (!version && !opts.fromPanel) {
        // Legacy caller: fetch published application releases for selection.
        let meta = null;
        try { meta = await api.getCloudWorkerMeta(); } catch { meta = null; }
        const versions = (meta?.releases || []).map((release) => release?.version).filter(Boolean);
        if (versions.length > 1) {
          const picked = window.prompt(
            `可用应用版本：\n${versions.join('\n')}\n\n输入要回滚到的版本：`,
            versions[0],
          );
          if (picked === null) return; // 用户取消
          version = picked.trim();
        } else if (versions.length === 1) {
          version = versions[0];
        }
      }
      if (!version) {
        setError('暂无可用的应用发布版本，请稍后刷新后重试');
        return;
      }
      const confirmed = await feedback.confirm({
        title: `回滚“${bot.display_name}”？`,
        message: `回滚会把云端虚拟员工切换到应用版本 ${version}，但会保留当前数据。`,
        confirmLabel: '确认回滚',
        tone: 'default',
      });
      if (!confirmed) return;
      setCloudActioning({ name, action: 'rollback' });
      await api.rollbackCloudWorker(name, { version });
      feedback.notify({ tone: 'success', message: '回滚已触发，稍后刷新查看状态' });
    } catch (e) {
      setError(cloudWorkerActionMessage(e, '回滚'));
      await loadBots({ silent: true }).catch(() => {});
    } finally {
      setCloudActioning(null);
    }
  };

  // Cloud-managed reset (destructive). The cloud panel performs a captcha
  // confirmation in the card (verified=true); the hub card uses the confirm
  // dialog. version is optional and defaults to the latest image.
  const handleCloudReset = async (bot, version = '', opts = {}) => {
    const name = bot.tenant_name;
    if (!name) return;
    if (!opts.verified) {
      const confirmed = await feedback.confirm({
        title: `重置“${bot.display_name}”？`,
        message: version
          ? `重置会销毁该云端虚拟员工的实例并从镜像版本 ${version} 重建，所有数据将丢失且无法恢复！`
          : '重置会销毁该云端虚拟员工的实例并从镜像重建，所有数据将丢失且无法恢复！',
        confirmLabel: '重置并清空数据',
        tone: 'danger',
      });
      if (!confirmed) return;
    }
    try {
      setCloudActioning({ name, action: 'reset' });
      await api.resetCloudWorker(name, version ? { version } : {});
      feedback.notify({ tone: 'success', message: '重置已触发，稍后刷新查看状态' });
    } catch (e) {
      setError(cloudWorkerActionMessage(e, '重置'));
      await loadBots({ silent: true }).catch(() => {});
    } finally {
      setCloudActioning(null);
    }
  };

  const handleSaveEdit = async (e) => {
    e.preventDefault();
    if (!editingBot) return;
    if (!isOwnedBot(editingBot)) {
      setError('只能管理自己创建的助手');
      return;
    }
    try {
      await api.updateBot(editingBot.id, {
        display_name: editingBot.newDisplayName,
        avatar_url: editingBot.newAvatarUrl,
        role: editingBot.newRole,
        description: editingBot.newDescription.trim(),
        artifact_upload_enabled: editingBot.newArtifactUploadEnabled,
      });
      await loadBots({ silent: true });
      if (onBotsChanged) onBotsChanged();
      setEditingBot(null);
      setTab('hub');
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleSetVisibility = async (bot, visibility) => {
    const botId = bot?.id || bot?.uid;
    if (!botId || !isOwnedBot(bot)) return;
    const nextVisibility = normalizeBotVisibility(visibility);
    try {
      setError('');
      await api.setBotVisibility(botId, nextVisibility);
      setBots(prev => prev.map(item => (
        (item.id === botId || item.uid === botId)
          ? { ...item, visibility: nextVisibility }
          : item
      )));
      setEditingBot(prev => (
        prev && (prev.id === botId || prev.uid === botId)
          ? { ...prev, visibility: nextVisibility }
          : prev
      ));
      if (onBotsChanged) onBotsChanged();
    } catch (e) {
      setError(e.message || '更新助手可见性失败');
    }
  };

  const wsUrl = getWebSocketURL();

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1000 }}>
      {/* Removed arbitrary background hardcoding to allow inheritance from the global .oc-modal V3 matrix */}
      <div
        ref={dialogRef}
        className={`oc-modal cc-agent-manager${tab === 'manage' ? ' cc-agent-manager-manage' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="cc-agent-manager-title"
        onClick={e => e.stopPropagation()}
      >

        <div className="oc-modal-header cc-agent-manager-header">
          <div className="cc-agent-manager-nav">
            <h3 id="cc-agent-manager-title" className="cc-agent-manager-title">
              <Bot size={17} /> AI 助手管理
            </h3>
          </div>
          <div className="cc-agent-manager-header-actions">
            {tab !== 'hub' && tab !== 'manage' && (
              <button
                type="button"
                className="cc-agent-manager-header-action"
                onClick={() => setTab('hub')}
              >
                <ArrowLeft size={14} aria-hidden="true" /> <span>助手列表</span>
              </button>
            )}
            <button className="cc-dialog-close" onClick={onClose} aria-label="关闭"><X size={18} /></button>
          </div>
        </div>

        <div className={`oc-modal-body cc-agent-manager-body${tab === 'success' ? ' cc-agent-manager-success-body' : ''}${tab === 'hub' && !hubCloudView ? ' cc-agent-manager-hub-body' : ''}`}>

          {/* HUB TAB */}
          {tab === 'hub' && (
            hubCloudView ? (
              <div className="cc-agent-cloud-manage">
                <div className="cc-agent-cloud-manage-head">
                  <button type="button" className="oc-btn oc-btn-default" onClick={() => setHubCloudView(false)}>
                    <ArrowLeft size={15} /> 返回助手列表
                  </button>
                  <h3><Cloud size={16} /> 云托管管理</h3>
                  <span>配额 · 创建 · 版本/回滚/重置/删除</span>
                </div>
                <CloudWorkerPanel
                  quota={cloudQuota}
                  quotaError={cloudQuotaError}
                  workers={cloudWorkers}
                  images={cloudImages}
                  releases={cloudReleases}
                  actions={cloudActions}
                  actioning={cloudActioning}
                  showHostingSwitch={false}
                  onCreate={handleCloudCreate}
                  onUpdate={handleCloudUpdate}
                  onRollback={handleCloudRollback}
                  onReset={handleCloudReset}
                  onDelete={handleDelete}
                  onSwitchMode={() => setHubCloudView(false)}
                />
              </div>
            ) : (
            <div className="cc-agent-hub">
              {loading ? (
                <div className="cc-agent-hub-state">加载中...</div>
              ) : bots.length === 0 ? (
                <div className="cc-agent-hub-empty">
                  <Bot size={48} strokeWidth={1.5} />
                  <strong>还没有你创建的 AI 助手</strong>
                  <p>
                    已添加的助手会保留在左侧 AI 助手列表，可直接移动端使用或移除。
                  </p>
                  <button className="oc-btn cc-agent-empty-action" onClick={openCreateTab}>创建新助手</button>
                </div>
              ) : (
                <>
                  <section className="cc-agent-overview" aria-label="助手概览">
                    <div className="cc-agent-overview-heading">
                      <strong>助手概览</strong>
                    </div>
                    <div className="cc-agent-overview-stats">
                      <div><strong>{botOverview.total}</strong><span>全部助手</span></div>
                      <div><strong>{botOverview.online}</strong><span>当前在线</span></div>
                      <div><strong>{botOverview.publiclySearchable}</strong><span>公开可搜索</span></div>
                      <div><strong>{botOverview.selfHosted}</strong><span>自托管</span></div>
                    </div>
                  </section>

                  {/* 云托管管理入口（云员工独有：有配额或已有云托管员工时显示） */}
                  {(cloudQuota?.enabled || cloudWorkers.length > 0) && (
                    <button
                      type="button"
                      className="cc-agent-cloud-manage-entry"
                      onClick={() => setHubCloudView(true)}
                    >
                      <Cloud size={15} />
                      <span>云托管管理</span>
                      <small>{cloudWorkers.length > 0 ? `${cloudWorkers.length} 个员工` : '配额 · 版本 · 回滚/重置/删除'}</small>
                    </button>
                  )}

                  <div className="v3-agent-grid cc-agent-hub-grid">
                    {bots.map(bot => {
                      const botId = bot.id || bot.uid;
                      const owned = isOwnedBot(bot);
                      return (
                      <div key={botId} className="v3-agent-card" style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', padding: 16, borderRadius: 12 }}>
                      <div className="v3-agent-header" style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                        <div className="v3-agent-avatar" style={{ width: 48, height: 48, borderRadius: 8, background: 'var(--v3-bg-sidebar)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 20, color: 'var(--v3-primary)' }}>
                          {(bot.display_name || bot.username || '?').charAt(0).toUpperCase()}
                        </div>
                        <div className="v3-agent-info" style={{ flex: 1, minWidth: 0 }}>
                          <div className="cc-agent-card-title-row">
                            <h4 style={{ margin: 0, fontSize: 16, color: 'var(--v3-text-name)' }}>{bot.display_name}</h4>
                            {owned && (
                              <span className={`v3-agent-visibility-badge ${normalizeBotVisibility(bot.visibility) === BOT_VISIBILITY.PRIVATE ? 'private' : 'public'}`}>
                                {normalizeBotVisibility(bot.visibility) === BOT_VISIBILITY.PRIVATE ? '私有' : '公开'}
                              </span>
                            )}
                          </div>
                          <span style={{ fontSize: 13, color: 'var(--v3-text-muted)' }}>@{bot.username} · uid {botId}</span>
                        </div>
                      </div>
                      <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 16, marginTop: 12 }}>
                        {owned
                          ? (bot.tenant_name
                              ? `我创建的 · 云托管${bot.cloud_version ? ` · 版本 ${bot.cloud_version}` : ''}`
                              : '我创建的 · 自托管')
                          : '已添加的助手'}
                      </div>
                      <div className="v3-agent-actions">
                        {owned && (
                          <button
                            type="button"
                            className="oc-btn oc-btn-default cc-agent-card-action cc-agent-card-manage"
                            onClick={() => {
                              setEditingBot(editableBot(bot));
                              setTab('manage');
                            }}
                          >
                            <Settings2 size={14} aria-hidden="true" />
                            管理
                          </button>
                        )}
                        {owned && (
                          <button
                            type="button"
                            className="oc-btn oc-btn-default cc-agent-card-action"
                            onClick={() => setEntryBot(bot)}
                            title="入口码"
                          >
                            <QrCode size={14} aria-hidden="true" />
                            入口码
                          </button>
                        )}
                        {owned && !bot.tenant_name && (
                          <button
                            type="button"
                            className="oc-btn oc-btn-default cc-agent-card-action"
                            onClick={() => handleCopyBotAPIKey(bot, `api_${botId}`)}
                            disabled={copyingBotKey === botId}
                          >
                            {copiedField === `api_${botId}` ? '已复制' : copyingBotKey === botId ? '加载中...' : '复制 Key'}
                          </button>
                        )}
                        {owned && bot.tenant_name && (
                          <button
                            type="button"
                            className="oc-btn oc-btn-default cc-agent-card-action"
                            onClick={() => setHubCloudView(true)}
                            title="选择版本并更新、回滚或重置"
                          >
                            <Cloud size={14} aria-hidden="true" /> 云托管管理
                          </button>
                        )}
                        <button
                          type="button"
                          className="oc-btn oc-btn-default cc-agent-card-action cc-agent-card-delete"
                          aria-label={`删除助手 ${bot.display_name || bot.username}`}
                          title="删除助手"
                          onClick={() => handleDelete(bot)}
                        >
                          <Trash2 size={14} aria-hidden="true" />
                        </button>
                      </div>
                    </div>
                      );
                    })}
                  </div>
                  <button type="button" className="cc-agent-hub-create" onClick={openCreateTab}>
                    <Plus size={15} aria-hidden="true" />
                    创建新助手
                  </button>
                </>
              )}
            </div>
            )
          )}

          {/* CREATE TAB */}
          {tab === 'create' && (
            createMode === CREATE_MODES.MANAGED ? (
              <CloudWorkerPanel
                quota={cloudQuota}
                quotaError={cloudQuotaError}
                workers={cloudWorkers}
                images={cloudImages}
                releases={cloudReleases}
                actions={cloudActions}
                actioning={cloudActioning}
                onCreate={handleCloudCreate}
                onUpdate={handleCloudUpdate}
                onRollback={handleCloudRollback}
                onReset={handleCloudReset}
                onDelete={handleDelete}
                onSwitchMode={() => setCreateMode(CREATE_MODES.SELF_HOSTED)}
              />
            ) : (
              <form onSubmit={handleCreate} className="cc-agent-create-form">
                <div className="cc-agent-create-intro">
                  <h2>创建你的专属助手</h2>
                  <p>先定义助手身份，再配置运行方式。</p>
                </div>

              <div className="cc-agent-create-grid">
                <section className="cc-agent-create-card cc-agent-basic-card">
                  <h3><FileCheck2 size={17} />基本信息</h3>
                  <label>
                    <span>助手名称 <b>*</b></span>
                    <input
                      type="text"
                      value={createForm.display_name}
                      onChange={(e) => setCreateForm({ ...createForm, display_name: e.target.value })}
                      placeholder="例如：代码审查助手"
                      className="oc-auth-input"
                      required
                      disabled={isSubmitting}
                    />
                  </label>
                  <label>
                    <span>定位模板 <b>*</b></span>
                    <div className="cc-agent-role-field">
                      <CustomSelect
                        ariaLabel="定位模板"
                        className="cc-agent-role-select"
                        density="comfortable"
                        menuClassName="cc-agent-role-options"
                        value={createForm.role}
                        disabled={isSubmitting}
                        onValueChange={(role) => setCreateForm({ ...createForm, role })}
                      >
                        {ASSISTANT_ROLES.map((role) => <option key={role.value} value={role.value}>{role.label}</option>)}
                      </CustomSelect>
                      <small className="cc-agent-role-guidance">
                        用于初始 Skill 推荐与能力画像，不会直接改变 Agent 行为。{selectedRole.description}
                      </small>
                    </div>
                  </label>
                  <label>
                    <span>用途说明 <small>选填</small></span>
                    <textarea
                      value={createForm.description}
                      onChange={(e) => setCreateForm({ ...createForm, description: e.target.value.slice(0, 500) })}
                      placeholder="说明这个助手解决什么问题，以及你希望它如何工作"
                    />
                    <em>{createForm.description.length}/500</em>
                  </label>
                </section>

                <section className="cc-agent-create-card cc-agent-skill-card">
                  <div className="cc-agent-skill-card-heading">
                    <h3><Puzzle size={17} />Skills</h3>
                  </div>
                  <div
                    className="cc-agent-skill-tabs"
                    role="tablist"
                    aria-label="Skill 分类"
                    data-active-tab={skillPanelTab}
                  >
                    <button
                      id="cc-agent-skills-selected-tab"
                      type="button"
                      role="tab"
                      data-skill-panel-tab="selected"
                      aria-selected={skillPanelTab === 'selected'}
                      aria-controls="cc-agent-skills-selected-panel"
                      tabIndex={skillPanelTab === 'selected' ? 0 : -1}
                      onClick={() => setSkillPanelTab('selected')}
                      onKeyDown={handleSkillPanelKeyDown}
                    >
                      <span>已选</span>
                      <span className="cc-agent-skill-tab-count">{selectedSkills.length}</span>
                    </button>
                    <button
                      id="cc-agent-skills-available-tab"
                      type="button"
                      role="tab"
                      data-skill-panel-tab="available"
                      aria-selected={skillPanelTab === 'available'}
                      aria-controls="cc-agent-skills-available-panel"
                      tabIndex={skillPanelTab === 'available' ? 0 : -1}
                      onClick={() => setSkillPanelTab('available')}
                      onKeyDown={handleSkillPanelKeyDown}
                    >
                      <span>可用</span>
                      <span className="cc-agent-skill-tab-count">{availableSkills.length}</span>
                    </button>
                  </div>
                  <div className="cc-agent-skill-sections">
                    {skillSyncNotice && (
                      <div className="cc-agent-skill-sync-feedback" role="status">
                        <CheckCircle size={14} aria-hidden="true" />
                        <span>{skillSyncNotice}</span>
                      </div>
                    )}
                    {skillPanelTab === 'selected' ? (
                    <section
                      id="cc-agent-skills-selected-panel"
                      className="cc-agent-skill-group cc-agent-selected-group"
                      role="tabpanel"
                      aria-labelledby="cc-agent-skills-selected-tab"
                    >
                      {selectedSkills.length > 0 ? (
                        <div className="cc-agent-selected-skills" aria-label="已选择的 Skills">
                          {selectedSkills.map((skill) => (
                            <div className="cc-agent-selected-skill" key={skill.skillId}>
                              <button
                                type="button"
                                className="cc-agent-skill-detail-trigger"
                                onClick={() => openSkillDetails(skill)}
                                aria-label={`查看 Skill ${skill.displayName || skill.skillId} 详情`}
                              >
                                <span className="cc-agent-selected-skill-icon"><Puzzle size={16} /></span>
                                <span className="cc-agent-selected-skill-copy">
                                  <strong>{skill.displayName || skill.skillId}</strong>
                                  <small>
                                    {skill.author || (skill.isLocalSkill ? '本地 Skill' : 'SkillHub')}
                                    {skill.latestVersion ? ` · v${skill.latestVersion}` : ''}
                                  </small>
                                </span>
                              </button>
                              <button
                                type="button"
                                className="cc-agent-skill-row-action"
                                aria-label={`从助手移除 Skill ${skill.displayName || skill.skillId}`}
                                title="从助手移除"
                                disabled={isSubmitting}
                                onClick={() => toggleSelectedSkill(skill)}
                              >
                                <X size={14} />
                              </button>
                            </div>
                          ))}
                        </div>
                      ) : (
                        <div className="cc-agent-skill-group-empty">
                          <strong>还没有选择 Skill</strong>
                          <small>从“可用”中添加这个助手需要的能力。</small>
                        </div>
                      )}
                    </section>
                    ) : (
                    <section
                      id="cc-agent-skills-available-panel"
                      className="cc-agent-skill-group cc-agent-available-group"
                      role="tabpanel"
                      aria-labelledby="cc-agent-skills-available-tab"
                    >
                      {skillRecommendationLoading && (
                        <div className="cc-agent-skill-recommendation-state" aria-live="polite">
                          <strong>正在匹配推荐 Skill…</strong>
                        </div>
                      )}
                      {skillRecommendationError && (
                        <div className="cc-agent-skill-recommendation-state" role="status">
                          <strong>推荐暂时不可用</strong>
                          <small>已安装的 Skill 仍可正常添加。</small>
                        </div>
                      )}
                      {availableSkills.length > 0 ? (
                        <div className="cc-agent-selected-skills" aria-label="可用的 Skills">
                          {availableSkills.map((skill) => {
                            const selected = selectedSkills.some((item) => item.skillId === skill.skillId);
                            const canAdd = skill.canBind !== false;
                            return (
                            <div className="cc-agent-selected-skill" key={skill.skillId}>
                              <button
                                type="button"
                                className="cc-agent-skill-detail-trigger"
                                onClick={() => openSkillDetails(skill)}
                                aria-label={`查看 Skill ${skill.displayName || skill.skillId} 详情`}
                              >
                                <span className="cc-agent-selected-skill-icon"><Puzzle size={16} /></span>
                                <span className="cc-agent-selected-skill-copy">
                                  <span className="cc-agent-skill-name-row">
                                    <strong title={skill.displayName || skill.skillId} translate="no">
                                      {skill.displayName || skill.skillId}
                                    </strong>
                                    {skill.isRecommended && <span className="cc-agent-skill-recommended-badge">推荐</span>}
                                  </span>
                                  <small>
                                    {skill.author || (skill.isLocalSkill ? '本地 Skill' : 'SkillHub')}
                                    {skill.latestVersion ? ` · v${skill.latestVersion}` : ''}
                                    {!canAdd ? ' · 仅本地' : ''}
                                  </small>
                                </span>
                              </button>
                              <button
                                type="button"
                                className={`cc-agent-skill-row-action${selected ? ' is-selected' : ''}`}
                                onClick={() => (canAdd ? toggleSelectedSkill(skill) : openSkillDetails(skill))}
                                aria-label={selected
                                  ? `取消选择 Skill ${skill.displayName || skill.skillId}`
                                  : canAdd
                                    ? `添加 Skill ${skill.displayName || skill.skillId} 到助手`
                                    : `同步并添加 Skill ${skill.displayName || skill.skillId}`}
                                title={selected ? '已选择，点击取消' : canAdd ? '添加到助手' : '同步并添加'}
                                disabled={isSubmitting || skillSyncingID === skill.skillId}
                              >
                                {selected ? <Check size={15} aria-hidden="true" /> : <Plus size={15} aria-hidden="true" />}
                              </button>
                            </div>
                            );
                          })}
                        </div>
                      ) : !skillRecommendationLoading && !localSkillsLoading && (
                        <div className="cc-agent-skill-group-empty">
                          <strong>暂无可用 Skill</strong>
                          <small>{localSkillsError ? '连接本地服务，或浏览 SkillHub 添加能力。' : '可以浏览完整目录并手动选择。'}</small>
                        </div>
                      )}
                    </section>
                    )}
                  </div>
                  <button
                    type="button"
                    className="cc-agent-add-skill"
                    onClick={openSkillPicker}
                    disabled={isSubmitting}
                  >
                    <Search size={15} /> 浏览全部 Skills
                  </button>
                </section>
              </div>
                <fieldset className="cc-agent-hosting">
                  <legend><span><Zap size={16} /></span>部署方式 <small>高级设置</small></legend>
                  <label className={createMode === CREATE_MODES.SELF_HOSTED ? 'active' : ''}>
                    <input type="radio" name="hosting" checked={createMode === CREATE_MODES.SELF_HOSTED} onChange={() => setCreateMode(CREATE_MODES.SELF_HOSTED)} />
                    <span><strong>自托管</strong><small>生成本地身份 Key，后续连接你的服务。</small></span>
                  </label>
                  <label className={createMode === CREATE_MODES.MANAGED ? 'active' : (cloudQuotaError || !cloudQuota || cloudQuota.remaining <= 0 ? 'disabled' : '')}>
                    <input
                      type="radio"
                      name="hosting"
                      checked={createMode === CREATE_MODES.MANAGED}
                      disabled={cloudQuotaError || !cloudQuota || cloudQuota.remaining <= 0}
                      onChange={() => setCreateMode(CREATE_MODES.MANAGED)}
                    />
                    <span>
                      <strong>云托管</strong>
                      <small>
                        {cloudQuotaError
                          ? '云端状态查询失败，请稍后重试'
                          : (cloudQuota && cloudQuota.enabled
                              ? `部署到云端虚拟员工（可创建 ${cloudQuota.remaining}/${cloudQuota.total}）`
                              : '云端部署当前未开放，请联系管理员开通')}
                      </small>
                    </span>
                  </label>
                </fieldset>

                <button type="submit" className="oc-btn oc-btn-primary cc-agent-create-submit" disabled={isSubmitting}>
                  {isSubmitting
                    ? (selectedSkills.length > 0 ? '正在创建并添加 Skill...' : '创建中...')
                    : '创建我的专属助手'}
                </button>
              </form>
            )
          )}

          {/* SUCCESS (API KEY) TAB */}
          {tab === 'success' && createdBot && (
            <div className="cc-agent-success-layout">
              <AgentCapabilityVisualization
                agentName={createdBot.display_name}
                role={createdProfile?.role || selectedRole}
                skills={createdProfile?.skills || selectedSkills}
              />

              <aside className="cc-agent-success-summary" aria-labelledby="cc-agent-success-title">
                <div className="cc-agent-success-mark" aria-hidden="true">
                  <CheckCircle size={28} strokeWidth={1.8} />
                </div>
                <span className="cc-agent-success-eyebrow">创建成功</span>
                <h2 id="cc-agent-success-title">{createdBot.display_name}</h2>
                <p>
                  {createdMode === CREATE_MODES.MANAGED
                    ? '云端虚拟员工正在准备，可直接使用。'
                    : '凭证已生成，现在可以连接到 XiaoBa。'}
                </p>

                {createdSkillWarning && (
                  <div className="cc-agent-skill-binding-warning" role="status">
                    {createdSkillWarning}
                  </div>
                )}

                {createdMode === CREATE_MODES.MANAGED ? (
                  <div className="cc-agent-success-managed-note">
                    已部署到云端虚拟员工，无需配置 API Key。
                  </div>
                ) : (
                  <div className="cc-agent-success-credentials">
                    <section>
                      <span>API Key</span>
                      <div>
                        <code>{createdBot.api_key}</code>
                        <button
                          type="button"
                          className="oc-btn oc-btn-default"
                          onClick={() => handleCopy('api', createdBot.api_key)}
                          aria-label="复制 API Key"
                        >
                          {copiedField === 'api' ? '已复制' : '复制'}
                        </button>
                      </div>
                    </section>
                    <section>
                      <span>WebSocket 连接地址</span>
                      <div>
                        <code>{wsUrl}</code>
                        <button
                          type="button"
                          className="oc-btn oc-btn-default"
                          onClick={() => handleCopy('ws', wsUrl)}
                          aria-label="复制 WebSocket 连接地址"
                        >
                          {copiedField === 'ws' ? '已复制' : '复制'}
                        </button>
                      </div>
                    </section>
                    <small>API Key 用于验证这个 Agent，请勿发送给不受信任的人。</small>
                  </div>
                )}

                <button
                  type="button"
                  className="oc-btn oc-btn-primary cc-agent-success-done"
                  onClick={() => setTab('hub')}
                >
                  返回助手列表
                </button>
              </aside>
            </div>
          )}

          {/* MANAGE / EDIT TAB */}
          {tab === 'manage' && editingBot && (
            <form className="cc-agent-manage-form" onSubmit={handleSaveEdit}>
              <div className="cc-agent-manage-sections">
                <AgentManageSection
                  id={`cc-agent-manage-basic-${editingBot.id || editingBot.uid}`}
                  title="基本信息"
                  summary={editingBot.newDisplayName || editingBot.display_name}
                  open={manageSection === 'basic'}
                  onToggle={() => setManageSection('basic')}
                  variant="tab"
                >
              <div className="cc-agent-manage-basic-layout">
                <div className="cc-agent-manage-basic-fields">
              <div className="oc-form-group cc-agent-manage-avatar-field" style={{ marginBottom: 24 }}>
                <label style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}>头像</label>
                <div className="cc-agent-manage-avatar-wrap">
                  <button
                    type="button"
                    className="cc-agent-manage-avatar-button"
                    onClick={() => avatarFileRef.current?.click()}
                    disabled={avatarUploading}
                    aria-label="更换头像"
                    aria-busy={avatarUploading}
                  >
                  <Avatar
                    name={editingBot.newDisplayName || editingBot.display_name}
                    src={editingBot.newAvatarUrl}
                    size={64}
                    isBot
                  />
                  </button>
                  <input
                    ref={avatarFileRef}
                    type="file"
                    accept={IMAGE_UPLOAD_ACCEPT}
                    style={{ display: 'none' }}
                    onChange={async (event) => {
                      const file = event.target.files?.[0];
                      if (!file) return;
                      const uploadBotId = String(editingBot.id || editingBot.uid);
                      const validationError = validateImageUpload(file);
                      if (validationError) {
                        setError(validationError);
                        event.target.value = '';
                        return;
                      }
                      setAvatarUploading(true);
                      setError('');
                      try {
                        const uploaded = await api.uploadFile(file, 'image');
                        setEditingBot(prev => (
                          prev && String(prev.id || prev.uid) === uploadBotId
                            ? { ...prev, newAvatarUrl: uploaded.url || '' }
                            : prev
                        ));
                      } catch (err) {
                        if (
                          editingBotRef.current
                          && String(editingBotRef.current.id || editingBotRef.current.uid) === uploadBotId
                        ) {
                          setError(err.message || 'Avatar upload failed');
                        }
                      } finally {
                        setAvatarUploading(false);
                        event.target.value = '';
                      }
                    }}
                  />
                </div>
              </div>

              <div className="oc-form-group" style={{ marginBottom: 0 }}>
                <label
                  htmlFor={`cc-agent-name-${editingBot.id || editingBot.uid}`}
                  style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}
                >
                  名称
                </label>
                <input
                  id={`cc-agent-name-${editingBot.id || editingBot.uid}`}
                  type="text"
                  value={editingBot.newDisplayName}
                  onChange={(e) => setEditingBot({ ...editingBot, newDisplayName: e.target.value })}
                  className="oc-auth-input"
                  style={{ width: '100%', padding: '12px 16px', fontSize: 15 }}
                  required
                />
              </div>

              <div className="oc-form-group cc-agent-manage-description-field" style={{ marginBottom: 0 }}>
                <label
                  htmlFor={`cc-agent-description-${editingBot.id || editingBot.uid}`}
                  style={{ display: 'block', marginBottom: 8, fontSize: 13, color: 'var(--v3-text-muted)' }}
                >
                  用途说明 <small>选填</small>
                </label>
                <textarea
                  id={`cc-agent-description-${editingBot.id || editingBot.uid}`}
                  className="oc-auth-input cc-agent-manage-description"
                  value={editingBot.newDescription}
                  onChange={(event) => setEditingBot({
                    ...editingBot,
                    newDescription: event.target.value.slice(0, 500),
                  })}
                  placeholder="说明这个助手解决什么问题，以及你希望它如何工作"
                />
                <span>{editingBot.newDescription.length}/500</span>
              </div>
                </div>

                <AgentCapabilityVisualization
                  agentName={editingBot.newDisplayName || editingBot.display_name}
                  role={editingBot.newRole}
                  skills={managedSkills.skills}
                  compact
                />
              </div>
                </AgentManageSection>

                <AgentManageSection
                  id={`cc-agent-manage-connection-${editingBot.id || editingBot.uid}`}
                  title="连接与凭证"
                  summary={editingBot.tenant_name ? '云托管无需配置' : 'API Key 与 WebSocket'}
                  icon={Code2}
                  open={manageSection === 'connection'}
                  onToggle={() => setManageSection('connection')}
                  variant="tab"
                >
                {editingBot.tenant_name ? (
                  <div className="cc-agent-success-managed-note">
                    这个助手由云端托管，无需配置 API Key 或 WebSocket 地址。
                  </div>
                ) : (
                  <div className="cc-agent-credentials">
                  <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>API Key</div>
                  <div className="cc-agent-credential-row" style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
                    <code className="cc-agent-credential-value" style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: editingBot.api_key ? 'var(--v3-primary)' : 'var(--v3-text-muted)', fontFamily: 'var(--cc-font-mono)', fontSize: 13, userSelect: 'all' }}>
                      {editingBot.api_key || '点击复制加载 API Key'}
                    </code>
                    <button
                      type="button"
                      className="oc-btn oc-btn-default"
                      onClick={() => handleCopyBotAPIKey(editingBot, 'api_edit')}
                      disabled={copyingBotKey === editingBot.id}
                    >
                      {copiedField === 'api_edit' ? '已复制' : copyingBotKey === editingBot.id ? '加载中...' : '复制'}
                    </button>
                  </div>

                  <div style={{ fontSize: 11, color: 'var(--v3-text-muted)', marginBottom: 8, letterSpacing: 0.5 }}>WebSocket 连接地址</div>
                  <div className="cc-agent-credential-row" style={{ display: 'flex', gap: 8 }}>
                    <code className="cc-agent-credential-value" style={{ flex: 1, background: '#111', padding: '10px 12px', borderRadius: 6, color: 'var(--v3-text-main)', fontFamily: 'var(--cc-font-mono)', fontSize: 13, userSelect: 'all' }}>
                      {wsUrl}
                    </code>
                    <button type="button" className="oc-btn oc-btn-default" onClick={() => handleCopy('ws_edit', wsUrl)}>
                      {copiedField === 'ws_edit' ? '已复制' : '复制'}
                    </button>
                  </div>
                </div>
                )}
                </AgentManageSection>

              <AgentManageSection
                id={`cc-agent-manage-collaboration-${editingBot.id || editingBot.uid}`}
                title="使用与协作"
                summary={botVisibilityLabel(editingBot.visibility)}
                icon={Settings2}
                open={manageSection === 'collaboration'}
                onToggle={() => setManageSection('collaboration')}
                variant="tab"
              >
              <div className="cc-agent-collaboration-grid">
                <section className="cc-agent-collaboration-card cc-agent-visibility-settings">
                  <div className="cc-agent-collaboration-heading">
                    <div>
                      <h3>好友添加方式</h3>
                      <p>控制其他用户能否找到并申请添加这个 Agent。</p>
                    </div>
                    <span className="cc-agent-collaboration-status">{botVisibilityLabel(editingBot.visibility)}</span>
                  </div>
                  <div className="cc-agent-collaboration-options">
                  <button
                    type="button"
                    className={`oc-btn ${normalizeBotVisibility(editingBot.visibility) === BOT_VISIBILITY.PUBLIC ? 'oc-btn-primary' : 'oc-btn-default'}`}
                    onClick={() => handleSetVisibility(editingBot, BOT_VISIBILITY.PUBLIC)}
                  >
                    公开可搜索
                  </button>
                  <button
                    type="button"
                    className={`oc-btn ${normalizeBotVisibility(editingBot.visibility) === BOT_VISIBILITY.PRIVATE ? 'oc-btn-primary' : 'oc-btn-default'}`}
                    onClick={() => handleSetVisibility(editingBot, BOT_VISIBILITY.PRIVATE)}
                  >
                    私有不可搜索
                  </button>
                  </div>
                </section>

                <section className="cc-agent-collaboration-card cc-agent-artifact-settings">
                  <div className="cc-agent-collaboration-heading">
                    <div>
                      <h3>共享成果</h3>
                      <p>成员上传后直接展示，无需审批；你可以在成果列表中下架内容。</p>
                    </div>
                    <span className="cc-agent-collaboration-status">
                      {artifactSummary.loading ? '读取中' : `${artifactSummary.count} 项`}
                    </span>
                  </div>

                  <div className="cc-agent-artifact-summary" aria-live="polite">
                    <Cloud size={17} aria-hidden="true" />
                    <div>
                      <strong>{artifactSummary.error || `共 ${artifactSummary.count} 项成果 · ${artifactSummary.uploaderCount} 位上传者`}</strong>
                      <span>所有者始终可以上传和管理全部成果</span>
                    </div>
                  </div>

                  <div className="cc-agent-artifact-controls">
                    <div className="cc-agent-artifact-policy-copy">
                      <strong>允许成员上传</strong>
                      <span>关闭后，普通成员只能查看已有成果</span>
                    </div>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={editingBot.newArtifactUploadEnabled}
                      aria-label="允许成员上传共享成果"
                      className="cc-agent-artifact-switch"
                      onClick={() => setEditingBot((current) => ({
                        ...current,
                        newArtifactUploadEnabled: !current.newArtifactUploadEnabled,
                      }))}
                    >
                      <span aria-hidden="true" />
                    </button>
                  </div>

                  <button
                    type="button"
                    className="oc-btn oc-btn-default cc-agent-manage-artifacts"
                    onClick={() => onOpenCloudArtifacts?.(editingBot.id || editingBot.uid, editingBot)}
                    disabled={!onOpenCloudArtifacts}
                  >
                    管理成果
                  </button>
                </section>
                </div>
              </AgentManageSection>

              <AgentManageSection
                id={`cc-agent-manage-behavior-${editingBot.id || editingBot.uid}`}
                title="行为与能力"
                summary={managedSkills.loading
                  ? '正在读取能力配置'
                  : managedSkills.error || `系统提示词 · ${managedSkills.count} 个 Skill`}
                icon={Puzzle}
                open={manageSection === 'behavior'}
                onToggle={() => setManageSection('behavior')}
                variant="tab"
              >
                <section className="cc-agent-positioning-card" aria-labelledby="cc-agent-positioning-title">
                  <h3 id="cc-agent-positioning-title">定位模板</h3>
                  <div className="cc-agent-positioning-control">
                    <CustomSelect
                      ariaLabel="定位模板"
                      className="cc-agent-manage-role-select cc-agent-positioning-select"
                      density="comfortable"
                      menuClassName="cc-agent-role-options"
                      value={editingBot.newRole}
                      onValueChange={(role) => setEditingBot({ ...editingBot, newRole: role })}
                    >
                      {ASSISTANT_ROLES.map((role) => (
                        <option key={role.value} value={role.value}>{role.label}</option>
                      ))}
                    </CustomSelect>
                  </div>
                </section>

                <AgentSystemPromptCard agent={editingBot} />

              <section className="cc-agent-capability-summary" aria-labelledby="cc-agent-capability-summary-title">
                <div className="cc-agent-capability-summary-icon" aria-hidden="true">
                  <Puzzle size={18} />
                </div>
                <div className="cc-agent-capability-summary-copy">
                  <h3 id="cc-agent-capability-summary-title">能力配置</h3>
                  <p>这个 Agent 当前启用的 Skills 统一在 SkillHub 中管理。</p>
                  <span aria-live="polite">
                    {managedSkills.loading
                      ? '正在读取…'
                      : managedSkills.error || `已启用 ${managedSkills.count} 个 Skill`}
                  </span>
                </div>
                <button
                  type="button"
                  className="oc-btn oc-btn-default cc-agent-open-skillhub"
                  onClick={() => onOpenSkillHub?.(editingBot.id || editingBot.uid, editingBot)}
                >
                  前往 SkillHub 管理
                </button>
              </section>
              </AgentManageSection>
              </div>

              <div className="cc-agent-manage-actions">
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }} onClick={() => setTab('hub')}>
                  取消
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }} onClick={() => setEntryBot(editingBot)}>
                  入口码
                </button>
                <button type="submit" className="oc-btn oc-btn-primary" style={{ flex: 1, padding: '14px 0', borderRadius: 8 }}>
                  保存
                </button>
              </div>
            </form>
          )}
        </div>

        {error && <InlineFeedback tone="error" className="cc-agent-inline-feedback">{error}</InlineFeedback>}
      </div>
      {entryBot && isOwnedBot(entryBot) && (
        <AgentEntryModal
          bot={entryBot}
          onClose={() => setEntryBot(null)}
          onCopy={handleCopy}
          copiedField={copiedField}
          onAccessChanged={() => loadBots({ silent: true })}
        />
      )}
      {skillDetail && typeof document !== 'undefined' && createPortal(
        <div
          className="oc-modal-overlay cc-agent-skill-detail-overlay"
          onClick={(event) => {
            event.stopPropagation();
            if (event.target === event.currentTarget) closeSkillDetails();
          }}
        >
          <section
            ref={skillDetailDialogRef}
            className="oc-modal cc-agent-skill-detail-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="cc-agent-skill-detail-title"
            aria-describedby="cc-agent-skill-detail-description"
            onClick={(event) => event.stopPropagation()}
          >
            <header className="cc-agent-skill-detail-header">
              <span aria-hidden="true"><Puzzle size={19} /></span>
              <div>
                <small>{skillDetail.skill.isLocalSkill ? '本地 Skill' : 'SkillHub Skill'}</small>
                <h2 id="cc-agent-skill-detail-title" translate="no">
                  {skillDetail.details?.displayName || skillDetail.skill.displayName || skillDetail.skill.skillId}
                </h2>
              </div>
              <button
                ref={skillDetailCloseRef}
                type="button"
                className="cc-dialog-close"
                onClick={closeSkillDetails}
                aria-label="关闭 Skill 详情"
              >
                <X size={18} />
              </button>
            </header>

            <div className="cc-agent-skill-detail-body">
              {skillDetail.loading && (
                <div className="cc-agent-skill-detail-status" role="status">正在读取最新参数…</div>
              )}
              {skillDetail.error && (
                <div className="cc-agent-skill-detail-status error" role="alert">{skillDetail.error}</div>
              )}
              {skillDetail.skill.isLocalSkill && skillDetail.skill.canBind === false && (
                <div className="cc-agent-skill-sync-note">
                  <strong>仅保存在本机</strong>
                  <span>
                    同步并添加会把这个 Skill 的可分享文件上传到 SkillHub。继续前请确认文件中不包含密钥或私密数据。
                  </span>
                </div>
              )}
              {skillSyncError && (
                <div className="cc-agent-skill-detail-status error" role="alert">{skillSyncError}</div>
              )}
              <p id="cc-agent-skill-detail-description">
                {skillDetail.details?.description || '该 Skill 暂无用途说明。'}
              </p>
              <dl>
                <div>
                  <dt>Skill ID</dt>
                  <dd><code translate="no">{skillDetail.skill.skillId}</code></dd>
                </div>
                <div>
                  <dt>推荐版本</dt>
                  <dd>
                    {skillDetail.details?.latestVersion
                      ? <code translate="no">v{skillDetail.details.latestVersion}</code>
                      : '待确认'}
                  </dd>
                </div>
                <div>
                  <dt>发布者</dt>
                  <dd>{skillDetail.details?.author || 'SkillHub'}</dd>
                </div>
              </dl>
            </div>

            <footer className="cc-agent-skill-detail-footer">
              <button type="button" className="oc-btn oc-btn-default" onClick={closeSkillDetails}>关闭</button>
              <button
                type="button"
                className="oc-btn oc-btn-primary"
                disabled={
                  selectedSkills.some((skill) => skill.skillId === skillDetail.skill.skillId)
                  || skillSyncingID === skillDetail.skill.skillId
                }
                onClick={() => {
                  if (skillDetail.skill.canBind === false) {
                    syncAndSelectLocalSkill(skillDetail.skill);
                  } else {
                    toggleSelectedSkill(skillDetail.skill);
                    closeSkillDetails();
                  }
                }}
              >
                {selectedSkills.some((skill) => skill.skillId === skillDetail.skill.skillId)
                  ? '已添加'
                  : skillDetail.skill.canBind === false
                    ? skillSyncingID === skillDetail.skill.skillId
                      ? '正在同步…'
                      : '同步并添加'
                    : '添加此 Skill'}
              </button>
            </footer>
          </section>
        </div>,
        document.body,
      )}
      {skillPickerOpen && typeof document !== 'undefined' && createPortal(
        <div
          className="oc-modal-overlay cc-agent-skill-picker-overlay"
          onClick={(event) => {
            event.stopPropagation();
            if (event.target === event.currentTarget) closeSkillPicker();
          }}
        >
          <section
            ref={skillPickerRef}
            className="oc-modal cc-agent-skill-picker"
            role="dialog"
            aria-modal="true"
            aria-labelledby="cc-agent-skill-picker-title"
            onClick={(event) => event.stopPropagation()}
          >
            <header className="cc-agent-skill-picker-header">
              <div>
                <h2 id="cc-agent-skill-picker-title"><Puzzle size={18} />添加 Skills</h2>
                <p>选择创建后立即绑定到助手的能力。</p>
              </div>
              <button
                type="button"
                className="cc-dialog-close"
                onClick={closeSkillPicker}
                aria-label="关闭 Skill 选择"
              >
                <X size={18} />
              </button>
            </header>

            <form
              className="cc-agent-skill-search"
              onSubmit={(event) => {
                event.preventDefault();
                loadSkillCatalogue(skillQuery);
              }}
            >
              <Search size={16} aria-hidden="true" />
              <input
                ref={skillPickerSearchRef}
                type="search"
                value={skillQuery}
                onChange={(event) => setSkillQuery(event.target.value)}
                placeholder="搜索 Skill 名称或用途"
                aria-label="搜索 Skill"
              />
              <button type="submit" disabled={skillCatalogueLoading}>
                {skillCatalogueLoading ? '搜索中' : '搜索'}
              </button>
            </form>

            <div className="cc-agent-skill-results" aria-live="polite">
              {skillCatalogueError ? (
                <div className="cc-agent-skill-picker-state error" role="alert">
                  <strong>无法读取 SkillHub</strong>
                  <span>{skillCatalogueError}</span>
                  <button type="button" onClick={() => loadSkillCatalogue(skillQuery)}>重试</button>
                </div>
              ) : skillCatalogueLoading ? (
                <div className="cc-agent-skill-picker-state">
                  <strong>正在读取 SkillHub…</strong>
                  <span>稍等片刻，可用能力马上出现。</span>
                </div>
              ) : skillCatalogue.length === 0 ? (
                <div className="cc-agent-skill-picker-state">
                  <strong>没有找到匹配的 Skill</strong>
                  <span>换个关键词，或清空搜索查看全部能力。</span>
                </div>
              ) : (
                skillCatalogue.map((skill) => {
                  const selected = selectedSkills.some((item) => item.skillId === skill.skillId);
                  return (
                    <button
                      type="button"
                      key={skill.skillId}
                      className={`cc-agent-skill-option${selected ? ' is-selected' : ''}`}
                      aria-pressed={selected}
                      onClick={() => toggleSelectedSkill(skill)}
                    >
                      <span className="cc-agent-skill-option-icon"><Puzzle size={17} /></span>
                      <span className="cc-agent-skill-option-copy">
                        <strong>{skill.displayName || skill.skillId}</strong>
                        <small>{skill.description || '暂无说明'}</small>
                        <em>
                          {skill.author || 'SkillHub'}
                          {skill.latestVersion ? ` · v${skill.latestVersion}` : ''}
                        </em>
                      </span>
                      <span className="cc-agent-skill-option-check" aria-hidden="true">
                        {selected && <Check size={15} />}
                      </span>
                    </button>
                  );
                })
              )}
            </div>

            <footer className="cc-agent-skill-picker-footer">
              <span>{selectedSkills.length > 0 ? `已选择 ${selectedSkills.length} 个 Skill` : '可以稍后再添加'}</span>
              <div>
                <button type="button" className="oc-btn oc-btn-default" onClick={closeSkillPicker}>取消</button>
                <button type="button" className="oc-btn oc-btn-primary" onClick={closeSkillPicker}>
                  完成{selectedSkills.length > 0 ? `（${selectedSkills.length}）` : ''}
                </button>
              </div>
            </footer>
          </section>
        </div>,
        document.body,
      )}
    </div>
  );
}

function mergeManageableBots(rawBots, rawAgents, rawFriends = []) {
  const byID = new Map();
  const add = (item, fallback = {}) => {
    const id = item?.id || item?.uid;
    if (!id) return;
    byID.set(String(id), {
      ...fallback,
      ...item,
      id,
      uid: item.uid || id,
      display_name: item.display_name || item.username || fallback.display_name || fallback.username || '未命名助手',
      username: item.username || fallback.username || `agent-${id}`,
      is_bot: true,
    });
  };

  rawBots.forEach((bot) => add(bot));
  rawAgents
    .filter((agent) => agent && (agent.is_bot === true || agent.relation === 'friend' || agent.relation === 'owner'))
    .forEach((agent) => {
      const id = agent.uid || agent.id;
      if (!id) return;
      const existing = byID.get(String(id));
      if (existing) {
        const owned = isOwnedBot(existing);
        byID.set(String(id), {
          ...existing,
          avatar_url: agent.avatar_url || existing.avatar_url,
          display_name: agent.display_name || existing.display_name,
          is_online: agent.is_online ?? existing.is_online,
          online: agent.online ?? existing.online,
          relation: owned ? existing.relation : (agent.relation || existing.relation),
          is_owner: owned ? true : (existing.is_owner || agent.relation === 'owner'),
        });
        return;
      }
      add(agent, {
        relation: agent.relation || 'friend',
        is_owner: agent.relation === 'owner',
        visibility: agent.visibility || 'friend',
      });
    });
  rawFriends
    .filter((friend) => friend && (friend.bot === true || friend.is_bot === true || friend.account_type === 'bot' || friend.accountType === 'bot'))
    .forEach((friend) => {
      const id = friend.uid || friend.id;
      if (!id || byID.has(String(id))) return;
      add({
        id,
        uid: id,
        username: friend.username,
        display_name: friend.display_name,
        avatar_url: friend.avatar_url,
        relation: 'friend',
        is_owner: false,
        visibility: 'friend',
        is_bot: true,
        is_online: friend.is_online || friend.online || false,
      });
    });

  return Array.from(byID.values()).sort((a, b) => {
    const leftOwned = isOwnedBot(a);
    const rightOwned = isOwnedBot(b);
    if (leftOwned !== rightOwned) return leftOwned ? -1 : 1;
    return String(a.display_name || '').localeCompare(String(b.display_name || ''));
  });
}

function AgentEntryModal({ bot, onClose, onCopy, copiedField, onAccessChanged }) {
  const feedback = useFeedback();
  const [channel, setChannel] = useState('weixin');
  const [channelAppIds, setChannelAppIds] = useState({ weixin: '', feishu: '', weixin_clawbot: '' });
  const [entries, setEntries] = useState([]);
  const [accessMode, setAccessMode] = useState(CHANNEL_AGENT_ACCESS_MODES.APPROVAL_REQUIRED);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [qrImageError, setQrImageError] = useState(false);
  const [pendingRequests, setPendingRequests] = useState([]);
  const [botFriends, setBotFriends] = useState([]);
  const [managedBindings, setManagedBindings] = useState([]);
  const [accessTab, setAccessTab] = useState('pending');
  const [pendingLoading, setPendingLoading] = useState(false);
  const [reviewingUID, setReviewingUID] = useState(null);
  const [removingFriendUID, setRemovingFriendUID] = useState(null);
  const [clawBotMobileLink, setClawBotMobileLink] = useState(null);
  const [clawBotMobileLoading, setClawBotMobileLoading] = useState(false);
  const [clawBotMobileError, setClawBotMobileError] = useState('');
  const [clawBotAuthStatus, setClawBotAuthStatus] = useState(null);
  const clawBotMobileRequestRef = useRef(0);
  const clawBotMobileMountedRef = useRef(false);
  const botId = bot?.id || bot?.uid;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    api.getAgentEntries(botId)
      .then((res) => {
        if (!cancelled) setEntries(res.entries || []);
      })
      .catch((err) => {
        if (!cancelled) setError(err.message || 'Failed to load entry codes');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [botId]);

  const loadPendingRequests = useCallback(async () => {
    if (!botId) return;
    try {
      setPendingLoading(true);
      const [pendingRes, bindingsRes, friendsRes] = await Promise.all([
        api.getPendingRequests(botId),
        api.getAgentChannelBindings(botId),
        api.getBotFriends(botId),
      ]);
      setPendingRequests(pendingRes.requests || []);
      setManagedBindings(bindingsRes.bindings || []);
      setBotFriends(friendsRes.friends || []);
    } catch (err) {
      console.warn('load agent pending requests:', err);
      setPendingRequests([]);
      setManagedBindings([]);
      setBotFriends([]);
    } finally {
      setPendingLoading(false);
    }
  }, [botId]);

  useEffect(() => {
    loadPendingRequests();
  }, [loadPendingRequests]);

  const channelAppId = channelAppIds[channel] || '';
  const managedChannelAppID = isManagedChannel(channel);
  const normalizedChannelAppId = managedChannelAppID ? '' : channelAppId.trim();
  const entryScopeMatches = (entry, targetChannel = channel, targetAppId = normalizedChannelAppId) => (
    normalizeChannel(entry.channel) === normalizeChannel(targetChannel)
    && (isManagedChannel(targetChannel) || (entry.channel_app_id || '') === targetAppId)
  );
  const selected = entries.find((entry) => (
    entryScopeMatches(entry)
    && normalizeChannelAgentAccessMode(entry.access_mode) === accessMode
  ));
  const selectedEntryID = selected?.id || '';
  const entryUrl = selected?.entry_url || '';
  const channelQrUrl = selected?.channel_qr_url || '';
  const qrValue = selected?.qr_value || '';
  const qrKind = selected?.qr_kind || '';
  const feishuOAuthUrl = selected?.feishu_oauth_url || '';
  const feishuEntryStatus = selected?.feishu_entry_status || null;
  const clawBotEntryStatus = selected?.clawbot_entry_status || null;
  const feishuEntryReasons = Array.isArray(feishuEntryStatus?.reasons) ? feishuEntryStatus.reasons : [];
  const clawBotEntryReasons = Array.isArray(clawBotEntryStatus?.reasons) ? clawBotEntryStatus.reasons : [];
  const isFeishuOAuthEntry = isFeishuChannel(channel) && qrKind === 'feishu_oauth_entry' && qrValue;
  const isFeishuNativeEntry = isFeishuChannel(channel) && qrKind === 'feishu_native_entry' && qrValue;
  const hasFeishuEntryQRCode = isFeishuOAuthEntry || isFeishuNativeEntry;
  const isClawBotEntry = isWeixinClawBotChannel(channel) && qrKind === 'weixin_clawbot_ilink_qr' && qrValue;
  const displayQrUrl = isWeixinOfficialChannel(channel) && channelQrUrl ? channelQrUrl : '';
  const displayUrl = displayQrUrl || (hasFeishuEntryQRCode ? qrValue : isClawBotEntry ? qrValue : (isFeishuChannel(channel) || isWeixinClawBotChannel(channel) ? '' : qrValue || entryUrl));
  const usesLocalEntryUrl = isPotentiallyPrivateEntryUrl(displayUrl);
  const needsWeixinConfig = isWeixinOfficialChannel(channel) && selected && !displayQrUrl;
  const needsFeishuNativeConfig = isFeishuChannel(channel) && selected && !hasFeishuEntryQRCode;
  const needsClawBotConfig = isWeixinClawBotChannel(channel) && selected && !isClawBotEntry;
  const clawBotMobileStatus = clawBotMobileLink?.entry?.clawbot_entry_status || null;
  const clawBotMobileReasons = Array.isArray(clawBotMobileStatus?.reasons)
    ? clawBotMobileStatus.reasons
    : clawBotEntryReasons;
  const clawBotMobileQrKind = clawBotMobileLink?.qr_kind || clawBotMobileLink?.entry?.qr_kind || '';
  const clawBotMobileQRValue = clawBotMobileQrKind === 'weixin_clawbot_ilink_qr'
    ? (clawBotMobileLink?.qr_value || clawBotMobileLink?.channel_qr_url || '')
    : '';
  const clawBotMobileQRCode = clawBotMobileStatus?.qrcode || '';
  const clawBotMobileSceneKey = clawBotMobileLink?.scene_key || '';

  useEffect(() => {
    setQrImageError(false);
  }, [displayQrUrl]);

  useEffect(() => {
    clawBotMobileMountedRef.current = true;
    return () => {
      clawBotMobileMountedRef.current = false;
      clawBotMobileRequestRef.current += 1;
    };
  }, []);

  useEffect(() => {
    clawBotMobileRequestRef.current += 1;
    setClawBotMobileLink(null);
    setClawBotMobileLoading(false);
    setClawBotMobileError('');
    setClawBotAuthStatus(null);
  }, [botId, channel, accessMode, selectedEntryID]);

  const loadClawBotMobileLink = useCallback(async () => {
    if (!botId || !clawBotMobileMountedRef.current) return;
    const requestSeq = clawBotMobileRequestRef.current + 1;
    clawBotMobileRequestRef.current = requestSeq;
    try {
      setClawBotMobileLoading(true);
      setClawBotMobileError('');
      setClawBotAuthStatus(null);
      setClawBotMobileLink(null);
      const res = await api.createChannelIdentityMobileLink(botId, 'weixin_clawbot', selectedEntryID);
      if (clawBotMobileRequestRef.current !== requestSeq || !clawBotMobileMountedRef.current) return;
      setClawBotMobileLink(res);
    } catch (err) {
      if (clawBotMobileRequestRef.current !== requestSeq || !clawBotMobileMountedRef.current) return;
      setClawBotMobileLink(null);
      setClawBotMobileError(err.message || '暂时无法生成微信 ClawBot 授权码');
    } finally {
      if (clawBotMobileRequestRef.current === requestSeq && clawBotMobileMountedRef.current) {
        setClawBotMobileLoading(false);
      }
    }
  }, [botId, selectedEntryID]);

  useEffect(() => {
    if (!needsClawBotConfig || !selectedEntryID || clawBotMobileLink || clawBotMobileLoading || clawBotMobileError) {
      return;
    }
    loadClawBotMobileLink();
  }, [needsClawBotConfig, selectedEntryID, clawBotMobileLink, clawBotMobileLoading, clawBotMobileError, loadClawBotMobileLink]);

  useEffect(() => {
    if (!needsClawBotConfig || !clawBotMobileSceneKey || !clawBotMobileQRCode || !clawBotMobileQRValue) return undefined;
    let cancelled = false;
    let timer = null;
    const poll = async () => {
      try {
        const res = await api.getWeixinClawBotQRCodeStatus(clawBotMobileSceneKey, clawBotMobileQRCode);
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
  }, [needsClawBotConfig, clawBotMobileSceneKey, clawBotMobileQRCode, clawBotMobileQRValue]);

  const handleGenerate = async () => {
    try {
      setSaving(true);
      setError('');
      const res = await api.createAgentEntry(botId, channel, normalizedChannelAppId, accessMode);
      const next = res.entry;
      setEntries((prev) => [next, ...prev.filter((entry) => (
        !(
          entryScopeMatches(entry, next.channel, isManagedChannel(next.channel) ? '' : (next.channel_app_id || ''))
          && normalizeChannelAgentAccessMode(entry.access_mode) === normalizeChannelAgentAccessMode(next.access_mode)
        )
      ))]);
      await loadPendingRequests();
    } catch (err) {
      setError(err.message || 'Failed to generate entry code');
    } finally {
      setSaving(false);
    }
  };

  const handleRegenerate = async () => {
    if (!selected) return;
    const confirmed = await feedback.confirm({
      title: '重新生成入口码？',
      message: '重新生成后，旧入口码会立即失效。',
      confirmLabel: '重新生成',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      setSaving(true);
      setError('');
      const res = await api.regenerateAgentEntry(selected.id);
      const next = res.entry;
      setEntries((prev) => [next, ...prev.filter((entry) => (
        entry.id !== selected.id
        && !(
          entryScopeMatches(entry, next.channel, isManagedChannel(next.channel) ? '' : (next.channel_app_id || ''))
          && normalizeChannelAgentAccessMode(entry.access_mode) === normalizeChannelAgentAccessMode(next.access_mode)
        )
      ))]);
      feedback.notify({ tone: 'success', message: '入口码已重新生成' });
    } catch (err) {
      setError(err.message || 'Failed to regenerate entry code');
    } finally {
      setSaving(false);
    }
  };

  const handleReviewRequest = async (request, action) => {
    const fromUID = request?.from_user_id;
    if (!fromUID || !botId) return;
    try {
      setReviewingUID(fromUID);
      setError('');
      if (action === 'accept') {
        await api.acceptAgentFriend(botId, fromUID);
      } else {
        await api.rejectAgentFriend(botId, fromUID);
      }
      await loadPendingRequests();
    } catch (err) {
      setError(err.message || 'Failed to review friend request');
    } finally {
      setReviewingUID(null);
    }
  };

  const handleRemoveBotFriend = async (friend) => {
    const friendUID = friend?.id || friend?.uid;
    if (!friendUID || !botId) return;
    const name = friend.display_name || friend.username || `用户 ${friendUID}`;
    const confirmed = await feedback.confirm({
      title: `移除“${name}”的使用权限？`,
      message: '移除后，该用户将无法继续使用这个虚拟员工。',
      confirmLabel: '移除权限',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      setRemovingFriendUID(friendUID);
      setError('');
      await api.removeBotFriend(botId, friendUID);
      await loadPendingRequests();
      if (onAccessChanged) onAccessChanged();
      feedback.notify({ tone: 'success', message: '使用权限已移除' });
    } catch (err) {
      setError(err.message || '移除使用者失败');
    } finally {
      setRemovingFriendUID(null);
    }
  };

  const bindingsByStatus = useMemo(() => {
    const buckets = { approved: [], rejected: [], needs_login: [], pending: [] };
    (managedBindings || []).filter((item) => (
      normalizeChannelAgentAccessMode(item.access_mode || item.entry_access_mode) === accessMode
      && normalizeChannel(item.channel || item.binding?.channel) === channel
      && (!selected?.id || Number(item.binding?.entry_id || item.entry_id || 0) === Number(selected.id))
    )).forEach((item) => {
      const status = item.status || item.binding?.status || '';
      if (status === 'approved' || status === 'active') buckets.approved.push(item);
      else if (status === 'rejected') buckets.rejected.push(item);
      else if (status === 'needs_login' || status === 'pending_login') buckets.needs_login.push(item);
      else if (status === 'pending' || status === 'pending_approval') buckets.pending.push(item);
    });
    return buckets;
  }, [managedBindings, accessMode, channel, selected]);

  useEffect(() => {
    if (
      accessTab === 'pending'
      && pendingRequests.length === 0
      && bindingsByStatus.pending.length === 0
      && (botFriends.length > 0 || bindingsByStatus.approved.length > 0)
    ) {
      setAccessTab('approved');
    }
  }, [accessTab, pendingRequests.length, bindingsByStatus.pending.length, botFriends.length, bindingsByStatus.approved.length]);

  const accessTabs = [
    ['pending', '待处理', pendingRequests.length + bindingsByStatus.pending.length],
    ['approved', '已授权', botFriends.length + bindingsByStatus.approved.length],
    ['rejected', '未通过', bindingsByStatus.rejected.length],
    ['needs_login', '待登录', bindingsByStatus.needs_login.length],
  ];

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1200 }}>
      <div className="oc-modal" onClick={e => e.stopPropagation()} style={{ width: 520, maxWidth: '94vw' }}>
        <div className="oc-modal-header" style={{ padding: '18px 22px', borderBottom: '1px solid var(--v3-border)' }}>
          <h3 style={{ margin: 0, fontSize: 17, color: 'var(--v3-text-name)', display: 'flex', alignItems: 'center', gap: 8 }}>
            <QrCode size={18} /> {bot.display_name} 入口码
          </h3>
          <button className="oc-btn-default" style={{ width: 28, height: 28, padding: 0, border: 'none', background: 'transparent' }} onClick={onClose}>×</button>
        </div>

        <div className="oc-modal-body" style={{ padding: 22 }}>
          <div style={{ display: 'flex', gap: 8, marginBottom: 18 }}>
            {CHANNEL_OPTIONS.map(({ value, label }) => (
              <button
                key={value}
                type="button"
                className={`oc-btn ${channel === value ? 'oc-btn-primary' : 'oc-btn-default'}`}
                style={{ flex: 1, padding: '9px 0', borderRadius: 8 }}
                onClick={() => setChannel(value)}
              >
                {label}
              </button>
            ))}
          </div>

          {!managedChannelAppID && (
            <div style={{ marginBottom: 16 }}>
              <label style={{ display: 'block', color: 'var(--v3-text-muted)', fontSize: 12, marginBottom: 8 }}>
                微信 AppID（可选）
              </label>
              <input
                value={channelAppId}
                onChange={(event) => setChannelAppIds((prev) => ({ ...prev, [channel]: event.target.value }))}
                className="oc-auth-input"
                placeholder="留空为通用入口码"
                style={{ width: '100%', padding: '10px 12px', fontSize: 13 }}
              />
            </div>
          )}

          <div style={{ marginBottom: 16 }}>
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, marginBottom: 8 }}>访问方式</div>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 8 }}>
              {[
                [CHANNEL_AGENT_ACCESS_MODES.APPROVAL_REQUIRED, '好友申请'],
                [CHANNEL_AGENT_ACCESS_MODES.PUBLIC, '公开访问'],
              ].map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  className={`oc-btn ${accessMode === value ? 'oc-btn-primary' : 'oc-btn-default'}`}
                  style={{ padding: '9px 0', borderRadius: 8 }}
                  onClick={() => setAccessMode(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.6, marginTop: 8 }}>
              {accessMode === CHANNEL_AGENT_ACCESS_MODES.PUBLIC
                ? '扫码后仍需登录 CatsCo 账号；账号验证通过后可直接对话，不需要管理员审批。设备操作只会使用申请人自己授权的设备。'
                : isFeishuChannel(channel)
                  ? '用户用飞书扫码后会打开该虚拟员工的飞书应用或机器人入口；首次进入会提交好友申请，通过并完成 CatsCo 账号绑定后，可直接在飞书对话并使用该账号已连接的设备。'
                  : isWeixinClawBotChannel(channel)
                    ? '用户用微信 ClawBot 扫码后会进入独立的 ClawBot 入口；首次进入会提交好友申请，通过后可直接在 ClawBot 对话。'
                    : '扫码后需要登录 CatsCo 并发送好友申请，通过后才能对话；设备操作只会使用申请人自己授权的设备。'}
            </div>
          </div>

          {error && <InlineFeedback tone="error" className="cc-agent-entry-feedback">{error}</InlineFeedback>}

          {loading ? (
            <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)' }}>正在读取入口码...</div>
          ) : selected && needsWeixinConfig ? (
            <div style={{ padding: 24, border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 10 }}>微信公众号入口码尚不可用</div>
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
                配置公众号 AppID、AppSecret 和服务器回调后，这里会显示可扫码关注并绑定虚拟员工的公众号参数二维码。
              </div>
              <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.6, marginBottom: 14 }}>
                公众号后台 URL：/api/channels/weixin/events<br />
                Token：CATSCO_WEIXIN_EVENT_TOKEN<br />
                消息加解密：明文或兼容模式
              </div>
              <div style={{ display: 'flex', gap: 8 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, entryUrl)}>
                  <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制测试链接'}
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={handleRegenerate} disabled={saving}>
                  <RefreshCw size={14} /> 重新生成
                </button>
              </div>
            </div>
          ) : selected && needsFeishuNativeConfig ? (
              <div style={{ padding: 24, border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 10 }}>飞书原生入口尚未配置</div>
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
                飞书扫码入口必须先打开飞书原生应用或机器人入口，并把 scene 带回 CatsCo 登录/申请流程。当前配置没有闭环，因此不会生成可投放二维码。
              </div>
              {feishuEntryReasons.length > 0 && (
                <div style={{ display: 'grid', gap: 6, marginBottom: 14 }}>
                  {feishuEntryReasons.map((reason, index) => (
                    <div key={`${reason}-${index}`} style={{ background: 'rgba(250,81,81,0.1)', color: '#fca5a5', border: '1px solid rgba(250,81,81,0.18)', borderRadius: 8, padding: '8px 10px', fontSize: 12, lineHeight: 1.45 }}>
                      {reason}
                    </div>
                  ))}
                </div>
              )}
              <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.6, marginBottom: 14 }}>
                必填环境变量：CATSCO_FEISHU_APP_ID、CATSCO_FEISHU_APP_SECRET、CATSCO_FEISHU_ENTRY_URL_TEMPLATE<br />
                模板建议把飞书原生入口最终指向 {'{landing_url_encoded}'}；也可以使用 {'{oauth_url_encoded}'} 或 {'{scene_key}'}。
              </div>
              {feishuEntryStatus?.oauth_callback_url && (
                <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.6, marginBottom: 14, wordBreak: 'break-all' }}>
                  飞书 OAuth 回调：{feishuEntryStatus.oauth_callback_url}<br />
                  飞书事件回调：{feishuEntryStatus.events_callback_url || '/api/channels/feishu/events'}
                </div>
              )}
              {feishuOAuthUrl && (
                <div style={{ background: 'rgba(59,130,246,0.12)', color: '#93c5fd', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                  OAuth 链接可以用于调试身份绑定；正式二维码会优先使用短链版本，确保扫码先完成 CatsCo 绑定。
                </div>
              )}
              <div style={{ display: 'flex', gap: 8 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`feishu_oauth_${selected.id}`, feishuOAuthUrl)} disabled={!feishuOAuthUrl}>
                  <Copy size={14} /> {copiedField === `feishu_oauth_${selected.id}` ? 'Copied!' : '复制 OAuth 辅助链接'}
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={handleRegenerate} disabled={saving}>
                  <RefreshCw size={14} /> 重新生成
                </button>
              </div>
            </div>
          ) : selected && needsClawBotConfig ? (
            <div style={{ padding: 24, border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
              <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, marginBottom: 10 }}>微信 ClawBot 授权码</div>
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 13, lineHeight: 1.7, marginBottom: 14 }}>
                这里会为当前登录用户生成 iLink 一次性授权二维码；它不是可投放的长期入口码。扫码授权后，服务端会保存本次授权返回的 token，之后该用户可在微信 ClawBot 里继续和这个机器人对话。
              </div>
              {clawBotMobileLoading && (
                <div style={{ padding: 32, textAlign: 'center', color: 'var(--v3-text-muted)', border: '1px solid var(--v3-border)', borderRadius: 8, marginBottom: 14 }}>
                  正在生成微信 ClawBot 授权码...
                </div>
              )}
              {!clawBotMobileLoading && clawBotMobileError && (
                <div style={{ background: 'rgba(250,81,81,0.1)', color: '#fca5a5', border: '1px solid rgba(250,81,81,0.18)', borderRadius: 8, padding: '10px 12px', fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                  {clawBotMobileError}
                </div>
              )}
              {!clawBotMobileLoading && clawBotMobileError && (
                <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
                  <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, entryUrl)} disabled={!entryUrl}>
                    <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制测试链接'}
                  </button>
                  <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={loadClawBotMobileLink}>
                    <RefreshCw size={14} /> 重试生成
                  </button>
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && clawBotMobileQRValue && (
                <div style={{ display: 'grid', gridTemplateColumns: '196px 1fr', gap: 18, alignItems: 'center', marginBottom: 14 }}>
                  <QRCode value={clawBotMobileQRValue} size={205} />
                  <div style={{ minWidth: 0 }}>
                    <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 8 }}>微信 ClawBot 一次性授权码</div>
                    <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 12 }}>
                      {clawBotMobileQRValue}
                    </div>
                    {clawBotMobileStatus?.qrcode_url && (
                      <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 12 }}>
                        ClawBot iLink 授权入口：{clawBotMobileStatus.qrcode_url}
                      </div>
                    )}
                    <div style={{
                      background: clawBotAuthStatus?.status === 'error' ? 'rgba(250,81,81,0.1)' : 'rgba(25,195,125,0.10)',
                      color: clawBotAuthStatus?.status === 'error' ? '#fca5a5' : '#19C37D',
                      border: clawBotAuthStatus?.status === 'error' ? '1px solid rgba(250,81,81,0.18)' : '1px solid rgba(25,195,125,0.18)',
                      borderRadius: 8,
                      padding: '9px 10px',
                      fontSize: 12,
                      lineHeight: 1.5,
                      marginBottom: 12,
                    }}>
                      {clawBotAuthStatus?.status === 'saved'
                        ? 'ClawBot 授权已保存；之后该用户可在微信 ClawBot 里继续和这个机器人对话。'
                        : clawBotAuthStatus?.status === 'error'
                          ? `正在重试授权状态检查：${clawBotAuthStatus.message}`
                          : '扫码后请保持这个窗口打开，授权确认后服务端会自动保存 token。'}
                    </div>
                    <div style={{ display: 'flex', gap: 8 }}>
                      <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`clawbot_mobile_${selected.id}`, clawBotMobileQRValue)}>
                        <Copy size={14} /> {copiedField === `clawbot_mobile_${selected.id}` ? 'Copied!' : '复制'}
                      </button>
                      <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={loadClawBotMobileLink} disabled={clawBotMobileLoading}>
                        <RefreshCw size={14} /> 重新生成
                      </button>
                    </div>
                  </div>
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && !clawBotMobileQRValue && clawBotMobileReasons.length > 0 && (
                <div style={{ display: 'grid', gap: 6, marginBottom: 14 }}>
                  {clawBotMobileReasons.map((reason, index) => (
                    <div key={`${reason}-${index}`} style={{ background: 'rgba(250,81,81,0.1)', color: '#fca5a5', border: '1px solid rgba(250,81,81,0.18)', borderRadius: 8, padding: '8px 10px', fontSize: 12, lineHeight: 1.45 }}>
                      {reason}
                    </div>
                  ))}
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && !clawBotMobileQRValue && (
                <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.6, marginBottom: 14 }}>
                可选环境变量：CATSCO_WEIXIN_CLAWBOT_ILINK_BASE_URL、CATSCO_WEIXIN_CLAWBOT_CDN_BASE_URL、CATSCO_WEIXIN_CLAWBOT_BOT_TYPE、CATSCO_WEIXIN_CLAWBOT_MEDIA_HOST_ALLOWLIST<br />
                默认使用 https://ilinkai.weixin.qq.com、https://novac2c.cdn.weixin.qq.com/c2c 和 bot_type=3。
                </div>
              )}
              {!clawBotMobileLoading && !clawBotMobileError && !clawBotMobileQRValue && (
                <div style={{ display: 'flex', gap: 8 }}>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, entryUrl)}>
                  <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制测试链接'}
                </button>
                <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={loadClawBotMobileLink} disabled={clawBotMobileLoading}>
                  <RefreshCw size={14} /> 生成授权码
                </button>
              </div>
              )}
            </div>
          ) : selected ? (
            <>
              <div style={{ display: 'grid', gridTemplateColumns: '196px 1fr', gap: 18, alignItems: 'center' }}>
                {displayQrUrl && !qrImageError ? (
                  <img
                    src={displayQrUrl}
                    alt={`${channelLabel(channel)}入口码`}
                    width={196}
                    height={196}
                    onError={() => setQrImageError(true)}
                    style={{ borderRadius: 8, background: '#fff', border: '1px solid var(--v3-border)', objectFit: 'contain' }}
                  />
                ) : (
                  <QRCode value={displayUrl} size={205} />
                )}
                <div style={{ minWidth: 0 }}>
                  <div style={{ fontSize: 12, color: 'var(--v3-text-muted)', marginBottom: 8 }}>
                    {qrKind === 'feishu_oauth_entry' ? '飞书 OAuth 绑定入口' : qrKind === 'feishu_native_entry' ? '飞书应用入口码' : qrKind === 'weixin_clawbot_ilink_qr' ? '微信 ClawBot 授权码' : displayQrUrl ? '微信公众号参数二维码' : '网页入口链接'}
                  </div>
                  <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-main)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                    {displayUrl}
                  </div>
                  {isWeixinOfficialChannel(channel) && qrImageError && (
                    <div style={{ background: 'rgba(250,81,81,0.1)', color: '#FA5151', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                      微信二维码加载失败，请检查 AppID/AppSecret、公众号接口权限、服务器 IP 白名单和微信后台消息加解密模式。
                    </div>
                  )}
                  {isFeishuChannel(channel) && hasFeishuEntryQRCode && feishuEntryStatus?.native_url && (
                    <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                      飞书原生入口：{feishuEntryStatus.native_url}
                    </div>
                  )}
                  {isWeixinClawBotChannel(channel) && clawBotEntryStatus?.qrcode_url && (
                    <div style={{ background: 'var(--v3-bg-app)', border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10, color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5, wordBreak: 'break-all', marginBottom: 14 }}>
                      ClawBot iLink 授权入口：{clawBotEntryStatus.qrcode_url}
                    </div>
                  )}
                  {usesLocalEntryUrl && (
                    <div style={{ background: 'rgba(245,158,11,0.12)', color: '#d97706', padding: 10, borderRadius: 8, fontSize: 12, lineHeight: 1.5, marginBottom: 14 }}>
                      当前入口链接不是公网 HTTPS 地址，手机扫码前需要配置公网访问地址。
                    </div>
                  )}
                  <div style={{ display: 'flex', gap: 8 }}>
                    <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={() => onCopy(`entry_${selected.id}`, displayUrl)}>
                      <Copy size={14} /> {copiedField === `entry_${selected.id}` ? 'Copied!' : '复制'}
                    </button>
                    <button type="button" className="oc-btn oc-btn-default" style={{ flex: 1, padding: '9px 0', borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }} onClick={handleRegenerate} disabled={saving}>
                      <RefreshCw size={14} /> 重新生成
                    </button>
                  </div>
                </div>
              </div>
              <div style={{ marginTop: 18, borderTop: '1px solid var(--v3-border)', paddingTop: 16 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
                    <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13 }}>访问管理</div>
                    <button type="button" className="oc-btn oc-btn-default" style={{ padding: '5px 9px', borderRadius: 8, fontSize: 12 }} onClick={loadPendingRequests} disabled={pendingLoading}>
                      {pendingLoading ? '刷新中' : '刷新'}
                    </button>
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 6, marginBottom: 10 }}>
                    {accessTabs.map(([value, label, count]) => (
                      <button
                        key={value}
                        type="button"
                        className={`oc-btn ${accessTab === value ? 'oc-btn-primary' : 'oc-btn-default'}`}
                        style={{ padding: '7px 0', borderRadius: 8, fontSize: 12 }}
                        onClick={() => setAccessTab(value)}
                      >
                        {label}{count ? ` ${count}` : ''}
                      </button>
                    ))}
                  </div>
                  {accessTab === 'pending' && (pendingRequests.length > 0 || bindingsByStatus.pending.length > 0) ? (
                    <div style={{ display: 'grid', gap: 8 }}>
                      {pendingRequests.map((request) => (
                        <div key={`${request.from_user_id}-${request.created_at || ''}`} style={{ display: 'grid', gridTemplateColumns: '1fr auto auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
                          <div style={{ minWidth: 0 }}>
                            <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                              {request.display_name || request.from_username || `用户 ${request.from_user_id}`}
                            </div>
                            <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>申请添加该虚拟员工</div>
                          </div>
                          <button type="button" className="oc-btn oc-btn-default" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'reject')} disabled={reviewingUID === request.from_user_id}>
                            <XCircle size={14} /> 拒绝
                          </button>
                          <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'accept')} disabled={reviewingUID === request.from_user_id}>
                            <CheckCircle size={14} /> 通过
                          </button>
                        </div>
                      ))}
                      {bindingsByStatus.pending.map((item) => (
                        <BindingStatusRow key={`pending-${item.binding?.id}`} item={item} note="已绑定 CatsCo，等待好友申请通过" />
                      ))}
                    </div>
                  ) : accessTab === 'pending' ? (
                    <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>暂无待处理申请。</div>
                  ) : accessTab === 'approved' && (botFriends.length > 0 || bindingsByStatus.approved.length > 0) ? (
                    <div style={{ display: 'grid', gap: 8 }}>
                      {botFriends.map((friend) => (
                        <BotFriendRow
                          key={`friend-${friend.id || friend.uid}`}
                          friend={friend}
                          removing={removingFriendUID === (friend.id || friend.uid)}
                          onRemove={() => handleRemoveBotFriend(friend)}
                        />
                      ))}
                      {bindingsByStatus.approved.map((item) => (
                        <BindingStatusRow
                          key={`approved-${item.binding?.id}`}
                          item={item}
                          note="已通过，可从对应渠道对话"
                        />
                      ))}
                    </div>
                  ) : bindingsByStatus[accessTab]?.length ? (
                    <div style={{ display: 'grid', gap: 8 }}>
                      {bindingsByStatus[accessTab].map((item) => (
                        <BindingStatusRow
                          key={`${accessTab}-${item.binding?.id}`}
                          item={item}
                          note={accessTab === 'approved' ? '已通过，可从对应渠道对话' : accessTab === 'rejected' ? '已拒绝，不能继续对话' : '已扫码，等待登录 CatsCo'}
                        />
                      ))}
                    </div>
                  ) : (
                    <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>暂无记录。</div>
                  )}
              </div>
            </>
          ) : (
            <>
              <div style={{ padding: 36, textAlign: 'center', border: '1px dashed var(--v3-border)', borderRadius: 8 }}>
                <div style={{ color: 'var(--v3-text-name)', marginBottom: 12 }}>还没有该渠道的入口码</div>
                <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '10px 18px', borderRadius: 8 }} onClick={handleGenerate} disabled={saving}>
                  {saving ? '正在生成...' : '生成入口码'}
                </button>
              </div>
              <div style={{ marginTop: 18, borderTop: '1px solid var(--v3-border)', paddingTop: 16 }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
                  <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13 }}>访问管理</div>
                  <button type="button" className="oc-btn oc-btn-default" style={{ padding: '5px 9px', borderRadius: 8, fontSize: 12 }} onClick={loadPendingRequests} disabled={pendingLoading}>
                    {pendingLoading ? '刷新中' : '刷新'}
                  </button>
                </div>
                {pendingRequests.length > 0 && (
                  <div style={{ display: 'grid', gap: 8, marginBottom: 10 }}>
                    {pendingRequests.map((request) => (
                      <div key={`${request.from_user_id}-${request.created_at || ''}`} style={{ display: 'grid', gridTemplateColumns: '1fr auto auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
                        <div style={{ minWidth: 0 }}>
                          <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                            {request.display_name || request.from_username || `用户 ${request.from_user_id}`}
                          </div>
                          <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>申请添加该虚拟员工</div>
                        </div>
                        <button type="button" className="oc-btn oc-btn-default" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'reject')} disabled={reviewingUID === request.from_user_id}>
                          <XCircle size={14} /> 拒绝
                        </button>
                        <button type="button" className="oc-btn oc-btn-primary" style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }} onClick={() => handleReviewRequest(request, 'accept')} disabled={reviewingUID === request.from_user_id}>
                          <CheckCircle size={14} /> 通过
                        </button>
                      </div>
                    ))}
                  </div>
                )}
                {botFriends.length > 0 ? (
                  <div style={{ display: 'grid', gap: 8 }}>
                    {botFriends.map((friend) => (
                      <BotFriendRow
                        key={`friend-empty-entry-${friend.id || friend.uid}`}
                        friend={friend}
                        removing={removingFriendUID === (friend.id || friend.uid)}
                        onRemove={() => handleRemoveBotFriend(friend)}
                      />
                    ))}
                  </div>
                ) : (
                  <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>暂无已授权使用者。</div>
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function BotFriendRow({ friend, removing, onRemove }) {
  const uid = friend?.id || friend?.uid;
  const name = friend?.display_name || friend?.username || `用户 ${uid || ''}`;
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
      <div style={{ minWidth: 0 }}>
        <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {name}
        </div>
        <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5 }}>
          CatsCo 好友 · UID {uid}
        </div>
      </div>
      <button
        type="button"
        className="oc-btn oc-btn-default"
        style={{ padding: '7px 9px', borderRadius: 8, display: 'flex', alignItems: 'center', gap: 5 }}
        onClick={onRemove}
        disabled={removing}
      >
        <XCircle size={14} /> {removing ? '移除中' : '移除'}
      </button>
    </div>
  );
}

function BindingStatusRow({ item, note }) {
  const binding = item?.binding || {};
  const user = item?.user || {};
  const name = user.display_name || user.username || binding.channel_user_id || `绑定 ${binding.id || ''}`;
  const channel = channelLabel(binding.channel || item?.channel);
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', alignItems: 'center', gap: 8, border: '1px solid var(--v3-border)', borderRadius: 8, padding: 10 }}>
      <div style={{ minWidth: 0 }}>
        <div style={{ color: 'var(--v3-text-name)', fontWeight: 700, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {name}
        </div>
        <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, lineHeight: 1.5 }}>
          {channel} · {note}
        </div>
      </div>
      <div style={{ color: 'var(--v3-text-muted)', fontSize: 12 }}>
        {item?.status || binding.status || ''}
      </div>
    </div>
  );
}

function isPotentiallyPrivateEntryUrl(value) {
  if (!value) return false;
  try {
    const url = new URL(value);
    const hostname = url.hostname.toLowerCase();
    return url.protocol !== 'https:'
      || hostname === 'localhost'
      || hostname === '0.0.0.0'
      || hostname === '::1'
      || hostname.startsWith('127.')
      || hostname.startsWith('10.')
      || hostname.startsWith('192.168.')
      || /^172\.(1[6-9]|2\d|3[0-1])\./.test(hostname)
      || !hostname.includes('.');
  } catch {
    return false;
  }
}
