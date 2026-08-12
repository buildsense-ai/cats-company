import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import { URL } from 'node:url';

const port = Number(process.env.MOCK_CATS_PORT || 6061);
const scenario = String(process.env.MOCK_CATS_SCENARIO || 'new').trim().toLowerCase();
const echoReplies = ['1', 'true', 'yes', 'on'].includes(String(process.env.MOCK_CATS_ECHO || '').trim().toLowerCase());
const requestedEchoReplyDelayMs = Number(process.env.MOCK_CATS_ECHO_DELAY_MS || 1800);
const echoReplyDelayMs = Number.isFinite(requestedEchoReplyDelayMs)
  ? Math.max(0, Math.min(10_000, requestedEchoReplyDelayMs))
  : 1800;
const tutorialTasksFile = String(process.env.MOCK_CATS_TUTORIAL_TASKS_FILE || '').trim();
const tutorialTasksJSON = String(process.env.MOCK_CATS_TUTORIAL_TASKS_JSON || '').trim();
const showcaseUsername = String(process.env.MOCK_CATS_SHOWCASE_USERNAME || 'ui-reviewer').trim();
const showcasePassword = String(process.env.MOCK_CATS_SHOWCASE_PASSWORD || 'demo123456');

let nextUserId = 100;
let nextBotId = 200;
let nextProjectId = 1;
let nextGroupId = 500;
const users = new Map();
const tokens = new Map();
const sessions = new Map();
const botsByOwner = new Map();
const relayKeysByUserId = new Map();
const onlineBodies = new Map();
const agentSockets = new Map();
const webSocketsByUserId = new Map();
const messagesByTopic = new Map();
const showcaseByUserId = new Map();
const projectsByUserId = new Map();
const projectTopicsByUserId = new Map();
const botModelConfigs = new Map();
const commercialOrdersByUserId = new Map();
let nextSeq = 1;

const mockCommercialPlans = [
  { id: 21, slug: 'catsco-personal', name: '个人版', description: '适合将 XiaoBa 作为日常个人助手。', price_fen: 39900, currency: 'CNY', sale_state: 'active', duration_days: 30 },
  { id: 22, slug: 'catsco-pro', name: '专业版', description: '适合高频、多任务并行或复杂工作。', price_fen: 79900, currency: 'CNY', sale_state: 'active', duration_days: 30 },
];

function mockCommercialOrders(userId) {
  const now = Date.now();
  const at = daysAgo => new Date(now - daysAgo * 24 * 60 * 60 * 1000).toISOString();
  return [
    ...(commercialOrdersByUserId.get(userId) || []),
    { order_no: 'CC202608110001PREVIEW', plan_id: 22, plan_name: '专业版', amount_fen: 79900, currency: 'CNY', channel: 'alipay_page', status: 'pending', checkout_url: 'https://openapi.alipay.test/gateway.do', expires_at: at(-1), created_at: at(0), updated_at: at(0) },
    { order_no: 'CC202608080002PREVIEW', plan_id: 21, plan_name: '个人版', amount_fen: 39900, currency: 'CNY', channel: 'alipay_page', status: 'fulfilled', paid_at: at(3), fulfilled_at: at(3), created_at: at(3), updated_at: at(3) },
    { order_no: 'CC202607020003PREVIEW', plan_id: 2, plan_name: 'Plus−', amount_fen: 4900, currency: 'CNY', channel: 'alipay_page', status: 'refunded', paid_at: at(40), created_at: at(40), updated_at: at(36) },
    { order_no: 'CC202606120004PREVIEW', plan_id: 1, plan_name: '3天体验', amount_fen: 990, currency: 'CNY', channel: 'alipay_page', status: 'closed', created_at: at(60), updated_at: at(60) },
  ];
}

const mockBotModels = [
  { id: 'minimax-m2.7', label: 'MiniMax M2.7', description: '标准额度，适合日常任务' },
  { id: 'minimax-m3', label: 'MiniMax M3', description: '支持多模态与长上下文' },
  {
    id: 'deepseek-v4-flash',
    label: 'DeepSeek V4 Flash',
    description: '低额度 Flash，支持推理强度',
    reasoning_efforts: ['high', 'max', 'disabled'],
    default_reasoning_effort: 'high',
  },
  ...['terra', 'sol', 'luna'].map((variant) => ({
    id: `gpt-5.6-${variant}`,
    label: `GPT-5.6 ${variant[0].toUpperCase()}${variant.slice(1)}`,
    description: 'OpenAI Responses，支持精细推理强度',
    provider: 'openai',
    protocol: 'OpenAI Responses',
    context_window_tokens: 1000000,
    reasoning_efforts: ['none', 'minimal', 'low', 'medium', 'high', 'xhigh'],
    default_reasoning_effort: 'medium',
  })),
];

function mockBotModelResponse(botId, includeModels = false) {
  const config = botModelConfigs.get(botId) || {
    configured: false,
    status: 'local',
    desired: { model_id: 'local', reasoning_effort: '', revision: 0 },
    applied: { model_id: '', reasoning_effort: '', revision: 0 },
    last_error: '',
  };
  return {
    uid: botId,
    management_enabled: true,
    runtime_supported: true,
    ...config,
    apply_mode: 'runtime_reload',
    ...(includeModels ? { models: mockBotModels } : {}),
  };
}

function p2pTopicId(uid1, uid2) {
  const a = Number(uid1);
  const b = Number(uid2);
  const [left, right] = a < b ? [a, b] : [b, a];
  return `p2p_${left}_${right}`;
}

function seedExistingBot(user) {
  if (scenario !== 'existing' && scenario !== 'showcase') return;
  const definitions = scenario === 'showcase'
    ? [
      { username: 'code_review_agent', display_name: '代码审查助手', model: 'gpt-5.6-terra', remaining_percent: 82 },
      { username: 'ops_data_agent', display_name: '运营数据助手', model: 'MiniMax-M3', remaining_percent: 61 },
      { username: 'research_agent', display_name: '行业研究助手', model: 'deepseek-v4-flash', remaining_percent: 47 },
      { username: 'content_agent', display_name: '内容策划助手', model: 'gpt-5.6-terra', remaining_percent: 28 },
      { username: 'quality_agent', display_name: '质量巡检助手', model: 'MiniMax-M2.7', remaining_percent: 93 },
    ]
    : [{ username: `existing_bot_${user.id}`, display_name: 'Existing Local Bot' }];
  const bots = definitions.map((definition) => {
    const id = nextBotId++;
    return {
      id,
      uid: id,
      username: definition.username,
      display_name: definition.display_name,
      avatar_url: '',
      api_key: `mock-api-key-${id}`,
      owner_id: user.id,
      quota_summary: definition.model ? {
        source: 'relay',
        model: definition.model,
        remaining_percent: definition.remaining_percent,
        status: 'normal',
      } : null,
      relation: 'owner',
      is_owner: true,
    };
  });
  botsByOwner.set(user.id, bots);
  if (scenario === 'showcase') seedChatShowcase(user, bots);
}

function seedChatShowcase(user, bots) {
  const now = Date.now();
  const at = (minutesAgo) => new Date(now - minutesAgo * 60_000).toISOString();
  const [codeAgent, opsAgent, researchAgent, contentAgent, qualityAgent] = bots;
  const friends = [
    { id: 301, uid: 301, username: 'linxiao', display_name: '林晓 · 产品设计', avatar_url: '', is_online: true, bot: false },
    { id: 302, uid: 302, username: 'chenyu', display_name: '陈宇 · 前端开发', avatar_url: '', is_online: false, bot: false },
    { id: 303, uid: 303, username: 'zhouqi', display_name: '周琦 · 增长运营', avatar_url: '', is_online: true, bot: false },
    { id: 304, uid: 304, username: 'wangmiao', display_name: '王淼 · 数据分析', avatar_url: '', is_online: true, bot: false },
    { id: 305, uid: 305, username: 'sunyi', display_name: '孙怡 · 品牌内容', avatar_url: '', is_online: false, bot: false },
    { id: 306, uid: 306, username: 'haoran', display_name: '郝然 · 后端开发', avatar_url: '', is_online: true, bot: false },
    { id: 307, uid: 307, username: 'fangning', display_name: '方宁 · 客户成功', avatar_url: '', is_online: false, bot: false },
    { id: 308, uid: 308, username: 'luyao', display_name: '陆遥 · 项目管理', avatar_url: '', is_online: true, bot: false },
  ];
  const groups = [
    {
      id: 401,
      topic_id: 'grp_401',
      name: 'CatsCo 前端验收群',
      avatar_url: '',
      owner_id: user.id,
      has_bot: true,
      member_count: 4,
      member_ids: [friends[0].id, friends[1].id, bots[0].id],
      created_at: at(24 * 60),
    },
    {
      id: 402,
      topic_id: 'grp_402',
      name: '产品与增长周会',
      avatar_url: '',
      owner_id: user.id,
      has_bot: false,
      member_count: 5,
      member_ids: [friends[0].id, friends[2].id, friends[3].id, friends[7].id],
      created_at: at(6 * 24 * 60),
    },
    {
      id: 403,
      topic_id: 'grp_403',
      name: '发布风险排查',
      avatar_url: '',
      owner_id: user.id,
      has_bot: true,
      member_count: 5,
      member_ids: [friends[1].id, friends[5].id, friends[7].id, qualityAgent.id],
      agent_id: qualityAgent.id,
      kind: 'agent_task',
      is_agent_task: true,
      created_at: at(2 * 24 * 60),
    },
    {
      id: 404,
      topic_id: 'grp_404',
      name: '品牌内容共创',
      avatar_url: '',
      owner_id: user.id,
      has_bot: false,
      member_count: 4,
      member_ids: [friends[0].id, friends[4].id, friends[6].id],
      created_at: at(4 * 24 * 60),
    },
    {
      id: 405,
      topic_id: 'grp_405',
      name: '客户声音同步群',
      avatar_url: '',
      owner_id: user.id,
      has_bot: false,
      member_count: 4,
      member_ids: [friends[2].id, friends[6].id, friends[7].id],
      created_at: at(8 * 24 * 60),
    },
    {
      id: 406,
      topic_id: 'grp_406',
      name: '竞品动态研究',
      avatar_url: '',
      owner_id: user.id,
      has_bot: true,
      member_count: 4,
      member_ids: [friends[2].id, friends[3].id, researchAgent.id],
      agent_id: researchAgent.id,
      kind: 'agent_task',
      is_agent_task: true,
      created_at: at(3 * 24 * 60),
    },
    {
      id: 407,
      topic_id: 'grp_407',
      name: '夏季发布会文案',
      avatar_url: '',
      owner_id: user.id,
      has_bot: true,
      member_count: 2,
      member_ids: [contentAgent.id],
      agent_id: contentAgent.id,
      kind: 'agent_task',
      is_agent_task: true,
      created_at: at(18 * 60),
    },
    {
      id: 408,
      topic_id: 'grp_408',
      name: '移动端回归测试',
      avatar_url: '',
      owner_id: user.id,
      has_bot: true,
      member_count: 3,
      member_ids: [friends[1].id, qualityAgent.id],
      agent_id: qualityAgent.id,
      kind: 'agent_task',
      is_agent_task: true,
      created_at: at(7 * 60),
    },
    {
      id: 409,
      topic_id: 'grp_409',
      name: '多 Agent 联合任务',
      avatar_url: '',
      owner_id: user.id,
      has_bot: true,
      member_count: 3,
      member_ids: [codeAgent.id, opsAgent.id],
      kind: 'agent_task',
      is_agent_task: true,
      created_at: at(5 * 60),
    },
    {
      id: 410,
      topic_id: 'grp_410',
      name: '等待分配 Agent',
      avatar_url: '',
      owner_id: user.id,
      has_bot: false,
      member_count: 2,
      member_ids: [friends[7].id],
      kind: 'agent_task',
      is_agent_task: true,
      created_at: at(4 * 60),
    },
  ];
  const codeTopic = p2pTopicId(user.id, codeAgent.id);
  const opsTopic = p2pTopicId(user.id, opsAgent.id);
  const researchTopic = p2pTopicId(user.id, researchAgent.id);
  const contentTopic = p2pTopicId(user.id, contentAgent.id);
  const qualityTopic = p2pTopicId(user.id, qualityAgent.id);
  const designTopic = p2pTopicId(user.id, friends[0].id);
  const frontendTopic = groups[0].topic_id;

  const seeded = [
    [codeTopic, [
      { from_uid: user.id, content: '请帮我检查新版聊天消息的布局，重点确认我的指令气泡、系统回复，以及两者之间的留白是否清晰。', created_at: at(11) },
      {
        from_uid: codeAgent.id,
        role: 'assistant',
        content: '可以。我会先检查用户指令是否靠右且使用独立气泡，再确认系统回复的头像、名称和正文层级，最后观察两类消息之间的垂直间距。',
        created_at: at(9),
      },
      { from_uid: user.id, content: '再用一条稍长的指令测试自动换行：气泡不要占满整行，文字上下需要有舒适留白，时间和操作按钮应位于气泡外部的右下方。', created_at: at(7) },
      {
        from_uid: codeAgent.id,
        role: 'assistant',
        content: '检查结果：长指令会在最大宽度内自然换行，气泡只包裹正文；时间、复制和更多操作位于气泡下方，并与右边缘对齐。',
        created_at: at(5),
      },
      { from_uid: user.id, content: '最后确认一下：时间和两个按钮只在鼠标悬浮时出现，按钮大小和间距与系统回复保持一致。', created_at: at(3) },
      {
        from_uid: codeAgent.id,
        role: 'assistant',
        content: '已确认。桌面端默认隐藏该操作行，悬浮或键盘聚焦时显示；触屏设备仍保留可操作入口。这组数据仅用于本地界面验收。',
        created_at: at(1),
      },
    ]],
    [frontendTopic, [
      { from_uid: friends[0].id, from_name: friends[0].display_name, content: '我把今天的视觉验收项整理好了，大家重点看深色模式。', created_at: at(28) },
      { from_uid: user.id, content: '好的，我正在用服务器生成的演示数据检查会话列表。', created_at: at(25) },
      { from_uid: codeAgent.id, from_name: codeAgent.display_name, role: 'assistant', content: '已记录 4 个检查点：侧栏密度、消息对齐、输入框状态、移动端折叠。', created_at: at(22) },
      { from_uid: friends[1].id, from_name: friends[1].display_name, content: '前端这边会再验证超长名称和多行预览。', created_at: at(18) },
    ]],
    [designTopic, [
      { from_uid: friends[0].id, from_name: friends[0].display_name, content: '侧栏二级标题的字重现在接近了，图标可以再弱一点。', created_at: at(16) },
      { from_uid: user.id, content: '明白，我会同时看展开和收起状态。', created_at: at(13) },
    ]],
    [opsTopic, [
      { from_uid: user.id, content: '生成一份本周活跃会话概览。', created_at: at(9) },
      { from_uid: opsAgent.id, role: 'assistant', content: '演示数据已准备：**18 个活跃会话**，其中私聊 9 个、群聊 5 个、Agent 会话 4 个。当前为本地模拟结果，不代表生产统计。', created_at: at(7) },
    ]],
    [researchTopic, [
      { from_uid: user.id, content: '整理近一个月协作类产品的更新，重点看 Agent、项目和知识库能力。', created_at: at(43) },
      { from_uid: researchAgent.id, role: 'assistant', content: '正在汇总公开信息，目前已完成 12 个产品的功能标签归类，并开始核对发布时间。', created_at: at(40) },
    ]],
    [contentTopic, [
      { from_uid: user.id, content: '把新版首页的核心卖点改写成三组短句，语气专业但不要太像广告。', created_at: at(96) },
      { from_uid: contentAgent.id, role: 'assistant', content: '第一轮草案已完成，但品牌术语表加载失败。我保留了草稿，等术语表恢复后可以继续统一措辞。', created_at: at(91) },
    ]],
    [qualityTopic, [
      { from_uid: user.id, content: '检查今天合并的侧栏交互，覆盖折叠、搜索、项目展开和任务状态。', created_at: at(14) },
      { from_uid: qualityAgent.id, role: 'assistant', content: '已完成 31 项检查，未发现阻塞问题；有 2 项窄屏视觉细节建议继续观察。', created_at: at(12) },
    ]],
    [p2pTopicId(user.id, friends[1].id), [
      { from_uid: friends[1].id, content: '移动端侧栏的滚动问题已经修好，等你一起看回归结果。', created_at: at(51) },
      { from_uid: user.id, content: '收到，我会连同项目展开状态一起检查。', created_at: at(49) },
    ]],
    [p2pTopicId(user.id, friends[2].id), [
      { from_uid: friends[2].id, content: '下周增长实验的名单我更新到了最终版，渠道备注也补全了。', created_at: at(3 * 60 + 12) },
      { from_uid: user.id, content: '好，明早周会直接用这版。', created_at: at(3 * 60 + 5) },
    ]],
    [p2pTopicId(user.id, friends[3].id), [
      { from_uid: user.id, content: '数据看板里转化率的口径能再确认一下吗？', created_at: at(6 * 60 + 20) },
      { from_uid: friends[3].id, content: '可以，今晚我把新旧口径对照和影响范围发给你。', created_at: at(6 * 60 + 4) },
    ]],
    [p2pTopicId(user.id, friends[4].id), [
      { from_uid: friends[4].id, content: '品牌手册新增了中文标点和数字格式规范，写作任务可以按新版走。', created_at: at(22 * 60) },
    ]],
    ['grp_402', [
      { from_uid: friends[2].id, content: '本周注册转化率回升了 6%，主要来自新手引导第二步。', created_at: at(2 * 60 + 15) },
      { from_uid: friends[7].id, content: '周会结论我已经整理成三个负责人和五个截止日期。', created_at: at(2 * 60) },
    ]],
    ['grp_403', [
      { from_uid: user.id, content: '请扫描本次发布清单，把高风险变更和缺少负责人的条目标出来。', created_at: at(34) },
      { from_uid: qualityAgent.id, role: 'assistant', content: '排查进行中：已核对 18/27 项，发现 2 个数据库变更仍缺少回滚说明。', created_at: at(31) },
    ]],
    ['grp_404', [
      { from_uid: friends[4].id, content: '发布会主视觉已经定稿，接下来集中收敛标题和社媒短文案。', created_at: at(5 * 60 + 35) },
      { from_uid: friends[0].id, content: '设计侧会给每套文案补一张安全区预览。', created_at: at(5 * 60 + 20) },
    ]],
    ['grp_405', [
      { from_uid: friends[6].id, content: '今天新增的客户反馈里，大家最关心的是任务记录能否快速归档。', created_at: at(26 * 60) },
    ]],
    ['grp_406', [
      { from_uid: user.id, content: '汇总本周竞品发布和价格变化，按影响程度分成三档。', created_at: at(4 * 60 + 15) },
      { from_uid: researchAgent.id, role: 'assistant', content: '研究已完成：共识别 9 项更新，其中 2 项高影响、3 项中影响、4 项低影响。', created_at: at(4 * 60) },
    ]],
    ['grp_407', [
      { from_uid: user.id, content: '为夏季发布会准备一个 90 秒开场稿，先给结构再写全文。', created_at: at(12 * 60 + 30) },
      { from_uid: contentAgent.id, role: 'assistant', content: '结构草案已生成，但资料引用校验未通过，需要补充最终产品数据后重试。', created_at: at(12 * 60 + 18) },
    ]],
    ['grp_408', [
      { from_uid: friends[1].id, content: '回归包已经上传，先从消息列表和输入框开始。', created_at: at(6) },
      { from_uid: qualityAgent.id, role: 'assistant', content: '正在执行移动端回归，目前完成 16/24 项，横屏和键盘弹起场景仍在检查。', created_at: at(4) },
    ]],
    ['grp_409', [
      { from_uid: user.id, content: '请代码和运营两个 Agent 一起复核本周发布数据。', created_at: at(9) },
      { from_uid: codeAgent.id, role: 'assistant', content: '代码侧检查已经开始。', created_at: at(8) },
      { from_uid: opsAgent.id, role: 'assistant', content: '运营数据口径正在同步核对。', created_at: at(7) },
    ]],
    ['grp_410', [
      { from_uid: user.id, content: '这个任务暂时还没有指定 Agent。', created_at: at(10) },
    ]],
  ];

  for (const [topic, messages] of seeded) {
    for (const message of messages) storeMessage(topic, message);
  }

  const conversations = [
    {
      ...conversationFromTopic(qualityTopic, '侧栏交互质量巡检', qualityAgent.id, false, at(12), true),
      task_status: { topic_id: qualityTopic, run_id: 'run-quality-1', state: 'completed', summary: '31 项检查已完成', updated_at: at(12) },
    },
    {
      ...conversationFromTopic('grp_408', groups[7].name, null, true, at(4), false, groups[7].id, true, groups[7].member_count),
      kind: 'agent_task',
      is_agent_task: true,
      task_status: { topic_id: 'grp_408', run_id: 'run-mobile-1', state: 'running', summary: '正在执行移动端回归：16/24', updated_at: at(4) },
    },
    {
      ...conversationFromTopic(opsTopic, '本周活跃会话概览', opsAgent.id, false, at(7), true),
      task_status: { topic_id: opsTopic, run_id: 'run-ops-1', state: 'completed', summary: '活跃会话概览已生成', updated_at: at(7) },
    },
    conversationFromTopic(designTopic, friends[0].display_name, friends[0].id, false, at(13)),
    conversationFromTopic(
      frontendTopic,
      groups[0].name,
      null,
      true,
      at(18),
      false,
      groups[0].id,
      groups[0].has_bot,
      groups[0].member_count,
    ),
    conversationFromTopic(codeTopic, '聊天气泡布局验收', codeAgent.id, false, at(1), true),
    {
      ...conversationFromTopic(researchTopic, '协作产品趋势研究', researchAgent.id, false, at(40), true),
      task_status: { topic_id: researchTopic, run_id: 'run-research-1', state: 'running', summary: '正在核对 12 个产品的更新', updated_at: at(40) },
    },
    {
      ...conversationFromTopic(contentTopic, '首页核心卖点改写', contentAgent.id, false, at(91), true),
      task_status: { topic_id: contentTopic, run_id: 'run-content-1', state: 'failed', error: '品牌术语表加载失败', updated_at: at(91) },
    },
    conversationFromTopic(p2pTopicId(user.id, friends[1].id), friends[1].display_name, friends[1].id, false, at(49)),
    conversationFromTopic(p2pTopicId(user.id, friends[2].id), friends[2].display_name, friends[2].id, false, at(3 * 60 + 5)),
    conversationFromTopic(p2pTopicId(user.id, friends[3].id), friends[3].display_name, friends[3].id, false, at(6 * 60 + 4)),
    conversationFromTopic(p2pTopicId(user.id, friends[4].id), friends[4].display_name, friends[4].id, false, at(22 * 60)),
    conversationFromTopic('grp_402', groups[1].name, null, true, at(2 * 60), false, groups[1].id, false, groups[1].member_count),
    {
      ...conversationFromTopic('grp_403', groups[2].name, null, true, at(31), false, groups[2].id, true, groups[2].member_count),
      kind: 'agent_task',
      is_agent_task: true,
      task_status: { topic_id: 'grp_403', run_id: 'run-release-1', state: 'running', summary: '正在核对发布清单：18/27', updated_at: at(31) },
    },
    conversationFromTopic('grp_404', groups[3].name, null, true, at(5 * 60 + 20), false, groups[3].id, false, groups[3].member_count),
    conversationFromTopic('grp_405', groups[4].name, null, true, at(26 * 60), false, groups[4].id, false, groups[4].member_count),
    {
      ...conversationFromTopic('grp_406', groups[5].name, null, true, at(4 * 60), false, groups[5].id, true, groups[5].member_count),
      kind: 'agent_task',
      is_agent_task: true,
      task_status: { topic_id: 'grp_406', run_id: 'run-competitor-1', state: 'completed', summary: '本周竞品研究已完成', updated_at: at(4 * 60) },
    },
    {
      ...conversationFromTopic('grp_407', groups[6].name, null, true, at(12 * 60 + 18), false, groups[6].id, true, groups[6].member_count),
      kind: 'agent_task',
      is_agent_task: true,
      task_status: { topic_id: 'grp_407', run_id: 'run-launch-copy-1', state: 'failed', error: '资料引用校验未通过', updated_at: at(12 * 60 + 18) },
    },
    {
      ...conversationFromTopic('grp_409', groups[8].name, null, true, at(7), false, groups[8].id, true, groups[8].member_count),
      kind: 'agent_task',
      is_agent_task: true,
    },
    {
      ...conversationFromTopic('grp_410', groups[9].name, null, true, at(10), false, groups[9].id, false, groups[9].member_count),
      kind: 'agent_task',
      is_agent_task: true,
    },
  ];
  const knownAgentIds = new Set(bots.map((bot) => Number(bot.id)));
  for (const group of groups) {
    group.agent_ids = [...new Set([
      group.agent_id,
      ...(Array.isArray(group.member_ids) ? group.member_ids : []),
    ].map(Number).filter((memberId) => knownAgentIds.has(memberId)))];
  }
  const groupsByTopic = new Map(groups.map((group) => [group.topic_id, group]));
  for (const conversation of conversations) {
    const group = groupsByTopic.get(conversation.id);
    if (!group) continue;
    conversation.agent_ids = [...group.agent_ids];
  }
  showcaseByUserId.set(user.id, { friends, groups, conversations });
  const projectDefinitions = [
    { name: 'CatsCo 体验优化', createdAt: at(14 * 24 * 60), updatedAt: at(5) },
    { name: '增长数据看板', createdAt: at(10 * 24 * 60), updatedAt: at(31) },
    { name: '2026 市场研究', createdAt: at(7 * 24 * 60), updatedAt: at(4 * 60) },
    { name: '品牌内容升级', createdAt: at(20 * 24 * 60), updatedAt: at(91) },
  ];
  const projects = projectDefinitions.map((definition) => ({
    id: nextProjectId++,
    owner_uid: user.id,
    name: definition.name,
    task_count: 0,
    created_at: definition.createdAt,
    updated_at: definition.updatedAt,
  }));
  projectsByUserId.set(user.id, projects);
  projectTopicsByUserId.set(user.id, new Map([
    [codeTopic, projects[0].id],
    [frontendTopic, projects[0].id],
    [opsTopic, projects[1].id],
    ['grp_403', projects[1].id],
    [researchTopic, projects[2].id],
    ['grp_406', projects[2].id],
    [contentTopic, projects[3].id],
    ['grp_407', projects[3].id],
  ]));
  refreshProjectAssignments(user.id);
}

function refreshProjectAssignments(userId) {
  const projects = projectsByUserId.get(userId) || [];
  const assignments = projectTopicsByUserId.get(userId) || new Map();
  const projectsById = new Map(projects.map((project) => [Number(project.id), project]));
  for (const project of projects) project.task_count = 0;
  for (const projectId of assignments.values()) {
    const project = projectsById.get(Number(projectId));
    if (project) project.task_count += 1;
  }
  const conversations = showcaseByUserId.get(userId)?.conversations || [];
  for (const conversation of conversations) {
    const project = projectsById.get(Number(assignments.get(conversation.id)));
    conversation.project_id = project?.id || 0;
    conversation.project_name = project?.name || '';
  }
}

function conversationFromTopic(
  id,
  name,
  friendId,
  isGroup,
  lastTime,
  isBot = false,
  groupId = null,
  hasBot = false,
  memberCount = 0,
) {
  const messages = messagesByTopic.get(id) || [];
  const latest = messages[messages.length - 1];
  return {
    id,
    friend_id: friendId,
    group_id: groupId,
    name,
    preview: latest?.content || '',
    last_time: lastTime || latest?.created_at || new Date().toISOString(),
    created_at: messages[0]?.created_at || new Date().toISOString(),
    is_group: isGroup,
    avatar_url: '',
    is_bot: isBot,
    has_bot: Boolean(hasBot),
    member_count: Number(memberCount) || 0,
    is_online: isBot || friendId === 301,
    latest_seq: latest?.seq || 0,
  };
}

function createUser(input) {
  const username = String(input.username || input.account || `local_user_${nextUserId}`).trim();
  const password = String(input.password || 'password123');
  const user = {
    id: nextUserId++,
    uid: nextUserId - 1,
    username,
    email: String(input.email || '').trim(),
    password,
    display_name: String(input.display_name || input.displayName || username).trim(),
    avatar_url: '',
    account_type: 'human',
  };
  users.set(user.username, user);
  if (user.email) users.set(user.email, user);
  seedExistingBot(user);
  return user;
}

function seedShowcaseAccount() {
  if (scenario !== 'showcase') return null;
  return createUser({
    username: showcaseUsername,
    email: `${showcaseUsername}@local.test`,
    password: showcasePassword,
    display_name: 'UI Reviewer',
  });
}

function issueToken(user) {
  const token = `mock-token-${user.id}-${crypto.randomBytes(8).toString('hex')}`;
  tokens.set(token, user);
  return token;
}

function userPayload(user, token) {
  return {
    token,
    uid: user.id,
    username: user.username,
    email: user.email,
    display_name: user.display_name,
    avatar_url: user.avatar_url,
    account_type: user.account_type,
  };
}

function resolveMockToken(token) {
  const known = tokens.get(token);
  if (known) return known;
  const match = /^mock-token-(\d+)-/.exec(String(token || ''));
  if (!match) return null;
  const userId = Number(match[1]);
  return [...new Set(users.values())].find((user) => user.id === userId) || null;
}

function getBearerUser(req) {
  const auth = String(req.headers.authorization || '');
  const token = auth.replace(/^Bearer\s+/i, '').trim();
  return resolveMockToken(token);
}

function getApiKeyBot(req) {
  const auth = String(req.headers.authorization || '');
  const headerKey = String(req.headers['x-api-key'] || '').trim();
  const apiKey = auth.replace(/^ApiKey\s+/i, '').trim() || headerKey;
  for (const bots of botsByOwner.values()) {
    const bot = bots.find((item) => item.api_key === apiKey);
    if (bot) return bot;
  }
  return null;
}

function findBotByTopic(topicId) {
  for (const [ownerId, bots] of botsByOwner.entries()) {
    const bot = bots.find((item) => p2pTopicId(ownerId, item.id) === topicId);
    if (bot) return { ownerId, bot };
  }
  for (const [ownerId, showcase] of showcaseByUserId.entries()) {
    const group = (showcase.groups || []).find((item) => item.topic_id === topicId && item.kind === 'agent_task');
    if (!group) continue;
    const bot = (botsByOwner.get(ownerId) || []).find((item) => item.id === group.agent_id);
    if (bot) return { ownerId, bot };
  }
  return null;
}

function storeMessage(topicId, message) {
  const topic = String(topicId || '').trim();
  if (!topic) return null;
  const seq = Number(message.seq || nextSeq++);
  const stored = {
    id: `${topic}-${seq}`,
    topic_id: topic,
    topic,
    from_uid: Number(message.from_uid || message.from || 0),
    from: String(message.from || message.from_uid || ''),
    content: message.content ?? '',
    content_blocks: message.content_blocks,
    type: message.type || message.msg_type || 'text',
    msg_type: message.msg_type || message.type || 'text',
    mode: message.mode,
    role: message.role,
    metadata: message.metadata,
    seq,
    created_at: message.created_at || new Date().toISOString(),
  };
  const list = messagesByTopic.get(topic) || [];
  list.push(stored);
  messagesByTopic.set(topic, list);
  return stored;
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let raw = '';
    req.on('data', (chunk) => {
      raw += chunk;
      if (raw.length > 2_000_000) req.destroy();
    });
    req.on('end', () => {
      if (!raw) return resolve({});
      try {
        resolve(JSON.parse(raw));
      } catch (error) {
        reject(error);
      }
    });
    req.on('error', reject);
  });
}

function send(res, status, payload) {
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Headers': 'Content-Type, Authorization, X-API-Key, X-CatsCo-Body-ID, X-CatsCo-Installation-ID',
    'Access-Control-Allow-Methods': 'GET, POST, PATCH, DELETE, OPTIONS',
  });
  res.end(JSON.stringify(payload));
}

function mockTutorialTasks() {
  if (tutorialTasksJSON) {
    try {
      const parsed = JSON.parse(stripJSONBOM(tutorialTasksJSON));
      if (parsed && Array.isArray(parsed.tasks)) return parsed;
    } catch (error) {
      return { tasks: [], limit: 12, error: `invalid MOCK_CATS_TUTORIAL_TASKS_JSON: ${error.message}` };
    }
  }
  if (tutorialTasksFile) {
    try {
      const parsed = JSON.parse(stripJSONBOM(fs.readFileSync(tutorialTasksFile, 'utf8')));
      if (parsed && Array.isArray(parsed.tasks)) return parsed;
    } catch (error) {
      return { tasks: [], limit: 12, error: `invalid MOCK_CATS_TUTORIAL_TASKS_FILE: ${error.message}` };
    }
  }
  return {
    limit: 12,
    tasks: [
      {
        id: 'read-image',
        title: '读图提取信息',
        intro: '下载一张示例图片，让 CatsCo 读取图片内容并整理出清晰要点。',
        files: [
          { name: 'catsco-tutorial-sample.png', url: '/demo-artifacts/catsco-tutorial-sample.png' },
        ],
        prompt: '请在我的下载文件夹中找到“catsco-tutorial-sample.png”，读取这张图片的内容，并帮我整理成清晰的要点。',
      },
      {
        id: 'move-image',
        title: '移动文件到桌面',
        intro: '下载同一张示例图片，让 CatsCo 在本机下载目录找到它，并安全移动到桌面。',
        files: [
          { name: 'catsco-tutorial-sample.png', url: '/demo-artifacts/catsco-tutorial-sample.png' },
        ],
        prompt: '请在我的下载文件夹中找到“catsco-tutorial-sample.png”，把它移动到桌面。完成后告诉我你移动前后的文件位置。如果桌面上已经有同名文件，请不要覆盖，改用一个安全的新文件名。',
      },
    ],
  };
}

function stripJSONBOM(value) {
  return String(value || '').replace(/^\uFEFF/, '');
}

function requireUser(req, res) {
  const user = getBearerUser(req);
  if (!user) {
    send(res, 401, { error: 'unauthorized' });
    return null;
  }
  return user;
}

async function handleApi(req, res) {
  if (req.method === 'OPTIONS') return send(res, 204, {});
  const url = new URL(req.url, `http://${req.headers.host || 'localhost'}`);

  try {
    if (req.method === 'GET' && url.pathname === '/health') {
      return send(res, 200, {
        ok: true,
        mode: 'local-onboarding-mock',
        scenario,
        echo_replies: echoReplies,
        echo_reply_delay_ms: echoReplyDelayMs,
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/admin/relay/access') {
      return send(res, 200, { allowed: false });
    }

    if (req.method === 'GET' && url.pathname === '/api/push/config') {
      return send(res, 200, { enabled: false });
    }

    if (req.method === 'POST' && url.pathname === '/__mock/reset') {
      users.clear();
      tokens.clear();
      sessions.clear();
      botsByOwner.clear();
      relayKeysByUserId.clear();
      onlineBodies.clear();
      agentSockets.clear();
      webSocketsByUserId.clear();
      messagesByTopic.clear();
      showcaseByUserId.clear();
      projectsByUserId.clear();
      projectTopicsByUserId.clear();
      commercialOrdersByUserId.clear();
      nextSeq = 1;
      nextUserId = 100;
      nextBotId = 200;
      nextProjectId = 1;
      nextGroupId = 500;
      seedShowcaseAccount();
      return send(res, 200, { ok: true, scenario });
    }

    if (req.method === 'GET' && url.pathname === '/__mock/state') {
      return send(res, 200, {
        users: [...new Set(users.values())].map((user) => ({ id: user.id, username: user.username })),
        bots: [...botsByOwner.values()].flat().map((bot) => ({
          id: bot.id,
          username: bot.username,
          owner_id: bot.owner_id,
          online: onlineBodies.has(bot.api_key),
          body_id: onlineBodies.get(bot.api_key) || '',
        })),
        sessions: sessions.size,
      });
    }

    if (req.method === 'POST' && url.pathname === '/api/auth/send-code') {
      const body = await readBody(req);
      const email = String(body.email || '').trim();
      if (!email || !email.includes('@')) return send(res, 400, { error: 'invalid email' });
      console.log(`[mock] verification code for ${email}: 123456`);
      return send(res, 200, { success: true, code: '123456' });
    }

    if (req.method === 'POST' && url.pathname === '/api/auth/register') {
      const body = await readBody(req);
      const username = String(body.username || body.email || '').trim();
      if (!username || String(body.password || '').length < 6) {
        return send(res, 400, { error: 'username min 3 chars, password min 6 chars' });
      }
      if (users.has(username)) return send(res, 409, { error: 'username taken' });
      const user = createUser(body);
      return send(res, 201, userPayload(user, issueToken(user)));
    }

    if (req.method === 'POST' && url.pathname === '/api/auth/login') {
      const body = await readBody(req);
      const account = String(body.account || body.username || '').trim();
      const user = users.get(account);
      if (!user || user.password !== String(body.password || '')) {
        return send(res, 401, { error: 'user not found or password mismatch' });
      }
      return send(res, 200, userPayload(user, issueToken(user)));
    }

    if (req.method === 'GET' && url.pathname === '/api/me') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, userPayload(user));
    }

    if (req.method === 'GET' && url.pathname === '/api/tutorial-tasks') {
      return send(res, 200, mockTutorialTasks());
    }

    if (req.method === 'POST' && url.pathname === '/api/desktop-connect/session') {
      const user = requireUser(req, res);
      if (!user) return;
      const code = crypto.randomBytes(18).toString('hex');
      const httpBaseURL = `http://localhost:${port}`;
      const serverURL = `ws://localhost:${port}/v0/channels`;
      const expiresAt = new Date(Date.now() + 5 * 60 * 1000).toISOString();
      sessions.set(code, { code, userId: user.id, expiresAt, claimed: false });
      return send(res, 200, {
        code,
        expires_at: expiresAt,
        http_base_url: httpBaseURL,
        server_url: serverURL,
        deeplink_url: `catsco://connect?code=${code}&base=${encodeURIComponent(httpBaseURL)}`,
      });
    }

    if (req.method === 'POST' && url.pathname === '/api/desktop-connect/exchange') {
      const body = await readBody(req);
      const session = sessions.get(String(body.code || '').trim());
      if (!session) return send(res, 404, { error: 'desktop connect session not found' });
      if (session.claimed) return send(res, 409, { error: 'desktop connect session already used' });
      session.claimed = true;
      const user = [...new Set(users.values())].find((item) => item.id === session.userId);
      if (!user) return send(res, 401, { error: 'invalid user' });
      return send(res, 200, {
        ...userPayload(user, issueToken(user)),
        http_base_url: `http://localhost:${port}`,
        server_url: `ws://localhost:${port}/v0/channels`,
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/desktop-connect/status') {
      const session = sessions.get(String(url.searchParams.get('code') || '').trim());
      if (!session) return send(res, 404, { error: 'desktop connect session not found' });
      return send(res, 200, { state: session.claimed ? 'claimed' : 'pending', expires_at: session.expiresAt });
    }

    if (req.method === 'GET' && url.pathname === '/api/agents') {
      const user = requireUser(req, res);
      if (!user) return;
      const bots = botsByOwner.get(user.id) || [];
      return send(res, 200, {
        agents: bots.map((bot) => ({
          uid: bot.id,
          id: bot.id,
          username: bot.username,
          display_name: bot.display_name,
          avatar_url: bot.avatar_url,
          relation: 'owner',
          is_bot: true,
          account_type: 'bot',
          is_online: scenario === 'showcase' || onlineBodies.has(bot.api_key),
          topic_id: p2pTopicId(user.id, bot.id),
        })),
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/agents/quota') {
      const user = requireUser(req, res);
      if (!user) return;
      const agentUid = Number(url.searchParams.get('uid'));
      const bot = (botsByOwner.get(user.id) || []).find((item) => item.id === agentUid);
      if (!bot) return send(res, 404, { error: 'agent not found' });
      return send(res, 200, {
        configured: Boolean(bot.quota_summary),
        shared: true,
        summary: bot.quota_summary || undefined,
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/devices') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, {
        devices: [],
        checked_at: Date.now(),
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/devices/audit') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, {
        events: [],
      });
    }

    if (req.method === 'POST' && url.pathname === '/api/agents/open') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const agentUid = Number(body.agent_uid || body.uid || body.id);
      const bot = (botsByOwner.get(user.id) || []).find((item) => item.id === agentUid);
      if (!bot) return send(res, 404, { error: 'agent not found' });
      return send(res, 200, {
        agent: {
          uid: bot.id,
          id: bot.id,
          username: bot.username,
          display_name: bot.display_name,
          avatar_url: bot.avatar_url,
          topic_id: p2pTopicId(user.id, bot.id),
        },
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/bots') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { bots: botsByOwner.get(user.id) || [] });
    }

    if (req.method === 'GET' && url.pathname === '/api/agents/quota') {
      const user = requireUser(req, res);
      if (!user) return;
      const uid = Number(url.searchParams.get('uid'));
      const bot = [...botsByOwner.values()].flat().find((item) => item.id === uid);
      if (!bot) return send(res, 404, { error: 'agent not found' });
      return send(res, 200, {
        configured: true,
        shared: true,
        summary: {
          source: 'relay',
          model: 'MiniMax-M2.7',
          remaining_percent: 72,
          status: 'normal',
        },
      });
    }

    if ((req.method === 'GET' || req.method === 'PATCH') && url.pathname === '/api/bots/model-config') {
      const user = requireUser(req, res);
      if (!user) return;
      const uid = Number(url.searchParams.get('uid'));
      const bot = (botsByOwner.get(user.id) || []).find((item) => item.id === uid);
      if (!bot) return send(res, 403, { error: 'not your bot' });
      if (req.method === 'PATCH') {
        const body = await readBody(req);
        const modelId = String(body.model_id || '').trim().toLowerCase();
        const model = mockBotModels.find((item) => item.id === modelId);
        if (modelId === 'local') {
          const current = mockBotModelResponse(uid);
          botModelConfigs.set(uid, {
            configured: false,
            status: 'local',
            desired: { model_id: 'local', reasoning_effort: '', revision: current.desired.revision + 1 },
            applied: current.applied,
            last_error: '',
          });
        } else if (model) {
          const requestedEffort = String(body.reasoning_effort || model.default_reasoning_effort || '').trim().toLowerCase();
          if (model.reasoning_efforts && !model.reasoning_efforts.includes(requestedEffort)) {
            return send(res, 400, { error: 'unsupported reasoning effort' });
          }
          const current = mockBotModelResponse(uid);
          botModelConfigs.set(uid, {
            configured: true,
            status: 'pending',
            desired: {
              model_id: model.id,
              reasoning_effort: model.reasoning_efforts ? requestedEffort : '',
              revision: current.desired.revision + 1,
            },
            applied: current.applied,
            last_error: '',
          });
        } else {
          return send(res, 400, { error: 'unsupported model' });
        }
      }
      return send(res, 200, mockBotModelResponse(uid, true));
    }

    if (req.method === 'GET' && url.pathname === '/api/bot/model-config') {
      const bot = getApiKeyBot(req);
      if (!bot) return send(res, 401, { error: 'bot api key required' });
      return send(res, 200, mockBotModelResponse(bot.id));
    }

    if (req.method === 'POST' && url.pathname === '/api/bot/model-config/ack') {
      const bot = getApiKeyBot(req);
      if (!bot) return send(res, 401, { error: 'bot api key required' });
      const body = await readBody(req);
      const current = mockBotModelResponse(bot.id);
      if (!current.configured || Number(body.revision) !== current.desired.revision) {
        return send(res, 409, { error: 'bot model configuration changed before it was applied' });
      }
      const applyError = String(body.error || '').trim();
      botModelConfigs.set(bot.id, {
        ...current,
        status: applyError ? 'failed' : 'applied',
        applied: applyError ? current.applied : {
          model_id: current.desired.model_id,
          reasoning_effort: current.desired.reasoning_effort,
          revision: current.desired.revision,
          applied_at: new Date().toISOString(),
        },
        last_error: applyError,
      });
      return send(res, 200, mockBotModelResponse(bot.id));
    }

    if (req.method === 'POST' && url.pathname === '/api/bots') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const bot = {
        id: nextBotId++,
        uid: nextBotId - 1,
        username: String(body.username || `catsco_${user.id}`).trim(),
        display_name: String(body.display_name || 'CatsCo').trim(),
        avatar_url: '',
        api_key: `mock-api-key-${nextBotId - 1}`,
        owner_id: user.id,
        relation: 'owner',
        is_owner: true,
      };
      botsByOwner.set(user.id, [...(botsByOwner.get(user.id) || []), bot]);
      return send(res, 201, { uid: bot.id, id: bot.id, api_key: bot.api_key, bot });
    }

    if (req.method === 'GET' && url.pathname === '/api/bots/api-key') {
      const user = requireUser(req, res);
      if (!user) return;
      const uid = Number(url.searchParams.get('uid'));
      const bot = (botsByOwner.get(user.id) || []).find((item) => item.id === uid);
      if (!bot) return send(res, 404, { error: 'bot not found' });
      return send(res, 200, { api_key: bot.api_key });
    }

    if (req.method === 'GET' && url.pathname === '/api/bots/body-status') {
      const user = requireUser(req, res);
      if (!user) return;
      const uid = Number(url.searchParams.get('uid'));
      const bot = (botsByOwner.get(user.id) || []).find((item) => item.id === uid);
      if (!bot) return send(res, 404, { error: 'bot not found' });
      const bodyId = onlineBodies.get(bot.api_key) || '';
      return send(res, 200, { active: Boolean(bodyId), body_id: bodyId });
    }

    if (req.method === 'POST' && url.pathname === '/api/friends/request') {
      return send(res, 200, { success: true });
    }

    if (req.method === 'POST' && url.pathname === '/api/friends/accept') {
      if (!getApiKeyBot(req) && !getBearerUser(req)) return send(res, 401, { error: 'unauthorized' });
      return send(res, 200, { success: true });
    }

    if (req.method === 'GET' && url.pathname === '/api/friends') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { friends: showcaseByUserId.get(user.id)?.friends || [] });
    }

    if (req.method === 'GET' && url.pathname === '/api/friends/pending') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { requests: [] });
    }

    if (req.method === 'GET' && url.pathname === '/api/users/online') {
      const user = requireUser(req, res);
      if (!user) return;
      const online = Object.fromEntries((showcaseByUserId.get(user.id)?.friends || []).map((friend) => [friend.id, Boolean(friend.is_online)]));
      return send(res, 200, { online });
    }

    if (req.method === 'POST' && url.pathname === '/api/groups/create') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const name = String(body.name || '').trim();
      const memberIds = Array.isArray(body.member_ids) ? body.member_ids.map(Number) : [];
      const kind = String(body.kind || 'standard').trim();
      const agent = (botsByOwner.get(user.id) || []).find((bot) => memberIds.includes(bot.id));
      if (!name) return send(res, 400, { error: 'group name required' });
      if (!['standard', 'agent_task'].includes(kind)) return send(res, 400, { error: 'invalid group kind' });
      if (kind === 'agent_task' && (memberIds.length !== 1 || !agent)) {
        return send(res, 400, { error: 'agent task requires exactly one agent' });
      }

      const showcase = showcaseByUserId.get(user.id) || { friends: [], groups: [], conversations: [] };
      showcaseByUserId.set(user.id, showcase);
      const id = nextGroupId++;
      const topicId = `grp_${id}`;
      const createdAt = new Date().toISOString();
      const normalizedMemberIds = [...new Set(memberIds.filter((memberId) => Number.isFinite(memberId) && memberId !== user.id))];
      const memberCount = 1 + normalizedMemberIds.length;
      const group = {
        id,
        topic_id: topicId,
        name,
        avatar_url: '',
        owner_id: user.id,
        has_bot: Boolean(agent),
        member_count: memberCount,
        member_ids: normalizedMemberIds,
        agent_id: agent?.id || null,
        agent_ids: agent ? [agent.id] : [],
        kind,
        is_agent_task: kind === 'agent_task',
        created_at: createdAt,
      };
      showcase.groups.unshift(group);
      showcase.conversations.unshift({
        ...conversationFromTopic(topicId, name, null, true, createdAt, false, id, Boolean(agent), memberCount),
        kind,
        is_agent_task: kind === 'agent_task',
        agent_ids: agent ? [agent.id] : [],
      });
      return send(res, 200, {
        group_id: id,
        topic: topicId,
        name,
        group,
        created_at: createdAt,
        avatar_url: '',
        kind,
        has_bot: Boolean(agent),
        is_agent_task: kind === 'agent_task',
        member_count: memberCount,
      });
    }

    if (req.method === 'POST' && url.pathname === '/api/groups/update') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const groupId = Number(body.group_id);
      const name = String(body.name || '').trim();
      const showcase = showcaseByUserId.get(user.id);
      const group = (showcase?.groups || []).find((item) => item.id === groupId);
      if (!group || !name) return send(res, 404, { error: 'group not found' });
      group.name = name;
      group.avatar_url = String(body.avatar_url || group.avatar_url || '');
      const conversation = showcase.conversations.find((item) => item.group_id === groupId);
      if (conversation) {
        conversation.name = name;
        conversation.avatar_url = group.avatar_url;
      }
      return send(res, 200, { ok: true, group });
    }

    if (req.method === 'GET' && url.pathname === '/api/groups') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { groups: showcaseByUserId.get(user.id)?.groups || [] });
    }

    if (req.method === 'GET' && url.pathname === '/api/groups/info') {
      const user = requireUser(req, res);
      if (!user) return;
      const showcase = showcaseByUserId.get(user.id);
      const groupId = Number(url.searchParams.get('id'));
      const group = (showcase?.groups || []).find((item) => item.id === groupId);
      if (!group) return send(res, 404, { error: 'group not found' });
      const groupMemberIds = new Set(Array.isArray(group.member_ids) ? group.member_ids.map(Number) : []);
      const members = [
        { user_id: user.id, display_name: user.display_name, username: user.username, role: 'owner', is_bot: false },
        ...(showcase?.friends || []).filter((friend) => groupMemberIds.has(friend.id)).map((friend) => ({
          user_id: friend.id,
          display_name: friend.display_name,
          username: friend.username,
          avatar_url: friend.avatar_url,
          role: 'member',
          is_bot: false,
        })),
        ...(botsByOwner.get(user.id) || []).filter((bot) => groupMemberIds.has(bot.id)).map((bot) => ({
          user_id: bot.id,
          display_name: bot.display_name,
          username: bot.username,
          avatar_url: bot.avatar_url,
          role: 'member',
          is_bot: true,
        })),
      ];
      return send(res, 200, { group, members });
    }

    if (req.method === 'GET' && url.pathname === '/api/relay/config') {
      return send(res, 200, {
        self_service_enabled: true,
        base_url: 'https://relay.catsco.cc',
        default_model: 'MiniMax-M2.7',
        models: [
          { id: 'minimax-m2.7', label: 'MiniMax M2.7', model: 'MiniMax-M2.7', provider: 'anthropic', base_url: 'https://relay.catsco.cc/anthropic', quota_class: 'standard', context_window_tokens: 204800, default: true },
          { id: 'minimax-m3', label: 'MiniMax M3', model: 'MiniMax-M3', provider: 'anthropic', base_url: 'https://relay.catsco.cc/anthropic', quota_class: 'multimodal', context_window_tokens: 1000000 },
          { id: 'deepseek-v4-flash', label: 'DeepSeek V4 Flash', model: 'deepseek-v4-flash', provider: 'anthropic', base_url: 'https://relay.catsco.cc/anthropic', quota_class: 'flash-low', context_window_tokens: 1000000 },
        ],
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/relay/usage') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, {
        configured: true,
        summary: {
          source: 'relay',
          model: String(url.searchParams.get('model') || 'MiniMax-M2.7'),
          quota_configured: true,
          used_cny: 3.2,
          limit_cny: 50,
          remaining_cny: 46.8,
          percent: 6.4,
          remaining_percent: 93.6,
          status: 'normal',
          reset_duration: '1M',
          last_reset: new Date(Date.now() - 8 * 24 * 60 * 60 * 1000).toISOString(),
        },
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/relay/commercial') {
      const user = requireUser(req, res);
      if (!user) return;
      if (scenario === 'showcase') {
        return send(res, 200, {
          enabled: true,
          enforce_enabled: true,
          note: '本地商业化界面演示',
          summary: {
            uid: user.id,
            models: ['gpt-5.6-luna', 'gpt-5.6-sol', 'gpt-5.6-terra'],
            entitlements: [{
              id: 'preview-entitlement',
              state: 'active',
              plan_id: 21,
              plan_slug: 'catsco-personal',
              plan_name: '个人版',
              starts_at: new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString(),
              expires_at: new Date(Date.now() + 27 * 24 * 60 * 60 * 1000).toISOString(),
            }],
          },
        });
      }
      return send(res, 200, { enabled: false, packages: [], invite: null });
    }

    if (req.method === 'GET' && url.pathname === '/api/relay/commercial/catalog') {
      const user = requireUser(req, res);
      if (!user) return;
      if (scenario !== 'showcase') return send(res, 200, { enabled: false, plans: [], channels: [], trial_available: false });
      return send(res, 200, {
        enabled: true,
        test_mode: false,
        trial_available: false,
        channels: [{ id: 'alipay_page', label: '支付宝支付', test_mode: false }],
        plans: mockCommercialPlans,
      });
    }

    if (req.method === 'GET' && url.pathname === '/api/relay/commercial/orders') {
      const user = requireUser(req, res);
      if (!user) return;
      const orders = scenario === 'showcase' ? mockCommercialOrders(user.id) : [];
      const orderNo = String(url.searchParams.get('order_no') || '').trim();
      if (orderNo) {
        const order = orders.find(item => item.order_no === orderNo);
        return order ? send(res, 200, { order }) : send(res, 404, { error: 'order not found' });
      }
      return send(res, 200, { orders });
    }

    if (req.method === 'POST' && url.pathname === '/api/relay/commercial/orders') {
      const user = requireUser(req, res);
      if (!user) return;
      if (scenario !== 'showcase') return send(res, 404, { error: 'commercial preview disabled' });
      const body = await readBody(req);
      const plan = mockCommercialPlans.find(item => item.id === Number(body.plan_id));
      if (!plan) return send(res, 404, { error: 'plan not found' });
      const now = new Date().toISOString();
      const order = {
        order_no: `CCPREVIEW${Date.now()}`,
        plan_id: plan.id,
        plan_name: plan.name,
        amount_fen: plan.price_fen,
        currency: plan.currency,
        channel: String(body.channel || 'alipay_page'),
        status: 'pending',
        checkout_url: 'https://openapi.alipay.test/gateway.do',
        created_at: now,
        updated_at: now,
      };
      commercialOrdersByUserId.set(user.id, [order, ...(commercialOrdersByUserId.get(user.id) || [])]);
      return send(res, 201, { order });
    }

    if (req.method === 'GET' && url.pathname === '/api/relay/key') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { key: relayKeysByUserId.get(user.id) || null });
    }

    if (req.method === 'POST' && (url.pathname === '/api/relay/key' || url.pathname === '/api/relay/key/rotate')) {
      const user = requireUser(req, res);
      if (!user) return;
      const key = {
        id: `mock-relay-key-${user.id}`,
        name: 'Mock local relay key',
        key: `sk-mock-relay-${user.id}-${crypto.randomBytes(8).toString('hex')}`,
        key_prefix: `sk-mock-relay-${user.id}`,
        state: 'active',
      };
      relayKeysByUserId.set(user.id, key);
      return send(res, 200, { key });
    }

    if (req.method === 'POST' && url.pathname === '/api/relay/key/reveal') {
      const user = requireUser(req, res);
      if (!user) return;
      const key = relayKeysByUserId.get(user.id);
      if (!key) return send(res, 404, { error: 'relay key not found' });
      return send(res, 200, { key });
    }

    if (req.method === 'DELETE' && url.pathname === '/api/relay/key') {
      const user = requireUser(req, res);
      if (!user) return;
      relayKeysByUserId.delete(user.id);
      return send(res, 200, { ok: true });
    }

    if (req.method === 'GET' && url.pathname === '/api/conversations') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { conversations: showcaseByUserId.get(user.id)?.conversations || [] });
    }

    if (req.method === 'GET' && url.pathname === '/api/projects') {
      const user = requireUser(req, res);
      if (!user) return;
      return send(res, 200, { projects: projectsByUserId.get(user.id) || [] });
    }

    if (req.method === 'POST' && url.pathname === '/api/projects') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const name = String(body.name || '').trim();
      if (!name || name.length > 128) return send(res, 400, { error: 'project name must be 1-128 characters' });
      const projects = projectsByUserId.get(user.id) || [];
      if (projects.some((project) => project.name === name)) return send(res, 409, { error: 'project name already exists' });
      const now = new Date().toISOString();
      const project = { id: nextProjectId++, owner_uid: user.id, name, task_count: 0, created_at: now, updated_at: now };
      projects.unshift(project);
      projectsByUserId.set(user.id, projects);
      return send(res, 201, { project });
    }

    if (req.method === 'PATCH' && url.pathname === '/api/projects') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const projectId = Number(body.project_id || 0);
      const name = String(body.name || '').trim();
      if (!projectId || !name || [...name].length > 128) return send(res, 400, { error: 'invalid project' });
      const projects = projectsByUserId.get(user.id) || [];
      const project = projects.find((item) => Number(item.id) === projectId);
      if (!project) return send(res, 404, { error: 'project not found' });
      if (projects.some((item) => Number(item.id) !== projectId && item.name === name)) {
        return send(res, 409, { error: 'project name already exists' });
      }
      project.name = name;
      project.updated_at = new Date().toISOString();
      refreshProjectAssignments(user.id);
      return send(res, 200, { ok: true });
    }

    if (req.method === 'DELETE' && url.pathname === '/api/projects') {
      const user = requireUser(req, res);
      if (!user) return;
      const projectId = Number(url.searchParams.get('project_id') || 0);
      const projects = projectsByUserId.get(user.id) || [];
      if (!projects.some((item) => Number(item.id) === projectId)) return send(res, 404, { error: 'project not found' });
      projectsByUserId.set(user.id, projects.filter((item) => Number(item.id) !== projectId));
      const assignments = projectTopicsByUserId.get(user.id) || new Map();
      for (const [topicId, assignedProjectId] of assignments.entries()) {
        if (Number(assignedProjectId) === projectId) assignments.delete(topicId);
      }
      projectTopicsByUserId.set(user.id, assignments);
      refreshProjectAssignments(user.id);
      return send(res, 200, { ok: true });
    }

    if (req.method === 'POST' && url.pathname === '/api/projects/topic') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const projectId = Number(body.project_id || 0);
      const topicId = String(body.topic_id || '').trim();
      const project = (projectsByUserId.get(user.id) || []).find((item) => Number(item.id) === projectId);
      const conversation = (showcaseByUserId.get(user.id)?.conversations || []).find((item) => item.id === topicId);
      if (!project || !conversation) return send(res, 404, { error: 'project or conversation not found' });
      const assignments = projectTopicsByUserId.get(user.id) || new Map();
      assignments.set(topicId, projectId);
      projectTopicsByUserId.set(user.id, assignments);
      project.updated_at = new Date().toISOString();
      refreshProjectAssignments(user.id);
      return send(res, 200, { ok: true });
    }

    if (req.method === 'DELETE' && url.pathname === '/api/projects/topic') {
      const user = requireUser(req, res);
      if (!user) return;
      const topicId = String(url.searchParams.get('topic_id') || '').trim();
      const assignments = projectTopicsByUserId.get(user.id) || new Map();
      assignments.delete(topicId);
      projectTopicsByUserId.set(user.id, assignments);
      refreshProjectAssignments(user.id);
      return send(res, 200, { ok: true });
    }

    if (req.method === 'PATCH' && url.pathname === '/api/conversations') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const topicId = String(body.topic_id || '').trim();
      const name = String(body.name || '').trim();
      if (!topicId.startsWith('p2p_') || !name || [...name].length > 80) {
        return send(res, 400, { error: 'invalid task name' });
      }
      const conversation = showcaseByUserId.get(user.id)?.conversations.find((item) => item.id === topicId);
      if (!conversation) return send(res, 404, { error: 'task not found' });
      conversation.name = name;
      return send(res, 200, { ok: true, topic_id: topicId, name });
    }

    if (req.method === 'GET' && url.pathname === '/api/messages') {
      const user = requireUser(req, res);
      if (!user) return;
      const topicId = String(url.searchParams.get('topic_id') || url.searchParams.get('topic') || '').trim();
      const limit = Number(url.searchParams.get('limit') || 50);
      const offset = Number(url.searchParams.get('offset') || 0);
      const list = topicId ? (messagesByTopic.get(topicId) || []) : [...messagesByTopic.values()].flat();
      const end = Math.max(0, list.length - offset);
      const start = Math.max(0, end - limit);
      return send(res, 200, { messages: list.slice(start, end) });
    }

    if (req.method === 'POST' && url.pathname === '/api/messages/send') {
      const user = requireUser(req, res);
      if (!user) return;
      const body = await readBody(req);
      const topicId = String(body.topic_id || body.topic || '').trim();
      const content = body.content ?? '';
      const match = findBotByTopic(topicId);
      console.log(`[mock] web message topic=${topicId || '-'} from=${user.id} text=${JSON.stringify(content).slice(0, 120)}`);
      storeMessage(topicId, {
        from_uid: user.id,
        from: user.id,
        content,
        content_blocks: body.content_blocks,
        type: body.type || 'text',
        msg_type: body.type || 'text',
      });
      const showcase = showcaseByUserId.get(user.id);
      const conversation = showcase?.conversations.find((item) => item.id === topicId);
      if (conversation) {
        conversation.preview = typeof content === 'string' ? content : JSON.stringify(content);
        conversation.last_time = new Date().toISOString();
        conversation.latest_seq = nextSeq - 1;
      }
      if (match) {
        const agentSocket = agentSockets.get(match.bot.api_key);
        if (agentSocket) {
          console.log(`[mock] forwarded message to agent uid=${match.bot.id}`);
          sendWS(agentSocket, {
            data: {
              topic: topicId,
              from: user.id,
              content,
              type: body.type || 'text',
              msg_type: body.type || 'text',
              seq: nextSeq++,
            },
          });
        } else {
          console.log(`[mock] no agent socket for bot uid=${match.bot.id}; message stored only`);
        }
        if (echoReplies) {
          broadcastToTopicOwner(topicId, {
            info: {
              topic: topicId,
              what: 'kp',
              from: `usr${match.bot.id}`,
            },
          });
          console.log(`[mock] typing indicator from bot uid=${match.bot.id}; echo in ${echoReplyDelayMs}ms`);
          setTimeout(() => {
            const echoMessage = storeMessage(topicId, {
              from_uid: match.bot.id,
              from: match.bot.id,
              content: `mock echo: ${typeof content === 'string' ? content : JSON.stringify(content)}`,
              type: 'text',
              msg_type: 'text',
              role: 'assistant',
            });
            const sockets = webSocketsByUserId.get(match.ownerId);
            console.log(`[mock] echo reply ${sockets?.size || 0} web socket(s)`);
            broadcastToTopicOwner(topicId, {
              data: {
                ...echoMessage,
                from: match.bot.id,
              },
            });
          }, echoReplyDelayMs);
        }
      } else {
        console.log(`[mock] no bot found for topic=${topicId || '-'}`);
      }
      return send(res, 200, { id: Date.now(), seq: nextSeq++ });
    }

    return send(res, 404, { error: `mock route not found: ${req.method} ${url.pathname}` });
  } catch (error) {
    return send(res, 500, { error: error.message || 'mock server error' });
  }
}

function wsAcceptValue(key) {
  return crypto
    .createHash('sha1')
    .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
    .digest('base64');
}

function encodeFrame(payload) {
  const body = Buffer.from(payload);
  if (body.length < 126) return Buffer.concat([Buffer.from([0x81, body.length]), body]);
  const header = Buffer.alloc(4);
  header[0] = 0x81;
  header[1] = 126;
  header.writeUInt16BE(body.length, 2);
  return Buffer.concat([header, body]);
}

function decodeFrame(buffer) {
  if (buffer.length < 2) return null;
  const opcode = buffer[0] & 0x0f;
  let offset = 2;
  let length = buffer[1] & 0x7f;
  if (length === 126) {
    if (buffer.length < 4) return null;
    length = buffer.readUInt16BE(2);
    offset = 4;
  } else if (length === 127) {
    return null;
  }
  const masked = Boolean(buffer[1] & 0x80);
  const mask = masked ? buffer.subarray(offset, offset + 4) : null;
  if (masked) offset += 4;
  const payload = buffer.subarray(offset, offset + length);
  if (payload.length < length) return null;
  if (opcode === 8) return { close: true };
  if (opcode === 9) return { ping: true };
  if (!masked) return payload.toString();
  const unmasked = Buffer.alloc(payload.length);
  for (let index = 0; index < payload.length; index += 1) {
    unmasked[index] = payload[index] ^ mask[index % 4];
  }
  return unmasked.toString();
}

function sendWS(socket, message) {
  socket.write(encodeFrame(JSON.stringify(message)));
}

function rememberWebSocket(user, socket) {
  if (!user) return;
  const sockets = webSocketsByUserId.get(user.id) || new Set();
  sockets.add(socket);
  webSocketsByUserId.set(user.id, sockets);
  socket.on('close', () => sockets.delete(socket));
  socket.on('error', () => sockets.delete(socket));
}

function broadcastToTopicOwner(topicId, message) {
  const match = findBotByTopic(topicId);
  if (!match) return;
  const sockets = webSocketsByUserId.get(match.ownerId);
  if (!sockets) return;
  for (const socket of sockets) {
    sendWS(socket, message);
  }
}

function urlTokenFromRequest(req) {
  try {
    const parsed = new URL(req.url, `http://${req.headers.host || 'localhost'}`);
    return String(parsed.searchParams.get('token') || '').trim();
  } catch {
    return '';
  }
}

function handleUpgrade(req, socket) {
  if (!String(req.url || '').startsWith('/v0/channels')) {
    socket.destroy();
    return;
  }
  const key = req.headers['sec-websocket-key'];
  if (!key) {
    socket.destroy();
    return;
  }
  const apiKey = String(req.headers['x-api-key'] || '').trim();
  const bodyId = String(req.headers['x-catsco-body-id'] || '').trim();
  const bot = apiKey ? getApiKeyBot(req) : null;
  const token = urlTokenFromRequest(req);
  const webUser = token ? resolveMockToken(token) : null;
  if (bot) {
    onlineBodies.set(apiKey, bodyId || 'mock-body');
    agentSockets.set(apiKey, socket);
    console.log(`[mock] bot online uid=${bot.id} username=${bot.username} body=${onlineBodies.get(apiKey)}`);
  } else if (webUser) {
    rememberWebSocket(webUser, socket);
    console.log(`[mock] web user online uid=${webUser.id} username=${webUser.username}`);
  }

  socket.write([
    'HTTP/1.1 101 Switching Protocols',
    'Upgrade: websocket',
    'Connection: Upgrade',
    `Sec-WebSocket-Accept: ${wsAcceptValue(key)}`,
    '',
    '',
  ].join('\r\n'));

  socket.on('data', (buffer) => {
    const decoded = decodeFrame(buffer);
    if (!decoded || decoded.close) return;
    if (decoded.ping) return;
    try {
      const msg = JSON.parse(decoded);
      if (msg.hi) {
        sendWS(socket, {
          ctrl: {
            id: msg.hi.id || '1',
            code: 200,
            text: 'ok',
            params: {
              build: 'catscompany',
              uid: bot?.id || 'web',
              name: bot?.display_name || 'Mock CatsCo',
              ver: 'mock',
              features: ['client_msg_id'],
            },
          },
        });
      } else if (msg.pub?.id) {
        sendWS(socket, { ctrl: { id: msg.pub.id, code: 200, text: 'ok', params: { seq: Date.now() } } });
        const stored = storeMessage(String(msg.pub.topic || ''), {
          from_uid: bot?.id || 0,
          from: `usr${bot?.id || 0}`,
          content: msg.pub.content ?? '',
          content_blocks: msg.pub.content_blocks,
          type: msg.pub.type || 'text',
          msg_type: msg.pub.msg_type || msg.pub.type || 'text',
          mode: msg.pub.mode,
          role: msg.pub.role || 'assistant',
          metadata: msg.pub.metadata,
        });
        broadcastToTopicOwner(String(msg.pub.topic || ''), {
          data: {
            ...stored,
          },
        });
      }
    } catch {
      // Ignore malformed mock frames.
    }
  });

  socket.on('close', () => {
    if (apiKey) onlineBodies.delete(apiKey);
    if (apiKey) agentSockets.delete(apiKey);
  });
  socket.on('error', () => {
    if (apiKey) onlineBodies.delete(apiKey);
    if (apiKey) agentSockets.delete(apiKey);
  });
}

const server = http.createServer(handleApi);
server.on('upgrade', handleUpgrade);
seedShowcaseAccount();
server.listen(port, '127.0.0.1', () => {
  console.log(`[mock] CatsCo local onboarding mock server listening on http://localhost:${port}`);
  console.log(`[mock] scenario=${scenario} (set MOCK_CATS_SCENARIO=new|existing|showcase)`);
  console.log(`[mock] echoReplies=${echoReplies} (set MOCK_CATS_ECHO=1 to echo without a real model)`);
  if (echoReplies) console.log(`[mock] echoReplyDelayMs=${echoReplyDelayMs}`);
  if (scenario === 'showcase') {
    console.log(`[mock] showcase login: ${showcaseUsername} / ${showcasePassword}`);
  }
});
