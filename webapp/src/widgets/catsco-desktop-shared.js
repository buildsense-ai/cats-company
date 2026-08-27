import { Apple, Laptop, Monitor } from 'lucide-react';

export const FALLBACK_RELEASE_VERSION = '1.4.1';
const TOS_BASE_URL = 'https://github-release.tos-cn-guangzhou.volces.com/update';

const DOWNLOAD_OPTION_DEFS = [
  {
    key: 'windows',
    title: 'Windows',
    description: '适用于 Windows 10/11 的安装程序',
    icon: Monitor,
    hrefForVersion: (version) => `${TOS_BASE_URL}/CatsCo-${version}-win.exe`,
    meta: 'x64 / arm64 由安装包自动适配',
  },
  {
    key: 'mac-arm',
    title: 'macOS Apple Silicon',
    description: '适用于 M 系列芯片 Mac',
    icon: Apple,
    hrefForVersion: (version) => `${TOS_BASE_URL}/macos-arm64/CatsCo-${version}-mac-arm64.dmg`,
    meta: 'arm64',
  },
  {
    key: 'mac-intel',
    title: 'macOS Intel',
    description: '适用于 Intel 芯片 Mac',
    icon: Apple,
    hrefForVersion: (version) => `${TOS_BASE_URL}/macos-x64/CatsCo-${version}-mac-x64.dmg`,
    meta: 'x64',
  },
  {
    key: 'linux-appimage',
    title: 'Linux AppImage',
    description: '无需安装，下载后赋予执行权限运行',
    icon: Laptop,
    hrefForVersion: (version) => `${TOS_BASE_URL}/CatsCo-${version}-linux.AppImage`,
    meta: 'x64',
  },
  {
    key: 'linux-deb',
    title: 'Linux Debian / Ubuntu',
    description: '适用于 Debian、Ubuntu 等发行版',
    icon: Laptop,
    hrefForVersion: (version) => `${TOS_BASE_URL}/CatsCo-${version}-linux.deb`,
    meta: 'deb',
  },
];

function safeReleaseHref(value) {
  const href = String(value || '').trim();
  return /^https?:\/\//i.test(href) ? href : '';
}

export function releaseVersion(release) {
  const version = String(release?.version || '').trim();
  return version || FALLBACK_RELEASE_VERSION;
}

export function buildDownloadOptions(release = {}) {
  const version = releaseVersion(release);
  const downloads = release?.downloads && typeof release.downloads === 'object' ? release.downloads : {};
  return DOWNLOAD_OPTION_DEFS.map(({ hrefForVersion, ...option }) => ({
    ...option,
    href: safeReleaseHref(downloads[option.key]) || hrefForVersion(version),
  }));
}

export const DOWNLOAD_OPTIONS = buildDownloadOptions({ version: FALLBACK_RELEASE_VERSION });

export function deviceStatusLabel(device) {
  if (device.routable) return '可用';
  if (device.routeConnected) return '已连接';
  if (device.active) return '活跃';
  return device.unavailableReason || device.status || '离线';
}

const HIDDEN_AUDIT_PHASES = new Set(['pairing_created']);

const AUDIT_PHASE_LABELS = {
  device_enrolled: '设备已连接',
  device_unlinked: '设备已解绑',
  rpc_forwarded: '任务已发送到设备',
  rpc_result: '设备任务完成',
  rpc_rejected: '设备任务未执行',
  rpc_result_rejected: '设备结果未接收',
};

const AUDIT_RESULT_LABELS = {
  denied: '已拒绝',
  duplicate: '重复请求',
  gone: '会话已断开',
  offline: '设备离线',
  rate_limited: '请求过多',
  unavailable: '设备不可用',
};

export function visibleDeviceAuditEvents(events) {
  return (Array.isArray(events) ? events : [])
    .filter((event) => event && !HIDDEN_AUDIT_PHASES.has(event.phase))
    .slice(0, 3);
}

export function auditTitle(event) {
  return AUDIT_PHASE_LABELS[event.phase] || event.phase || '设备活动';
}

export function auditDescription(event) {
  return event.device_id || event.operation || event.reason || AUDIT_RESULT_LABELS[event.result] || '设备活动';
}

export function auditMeta(event) {
  if (!event.result || event.result === 'ok') return '';
  return AUDIT_RESULT_LABELS[event.result] || event.result;
}
