import { request } from '../auth-session';

const PLAN_INVITE_TIMEOUT_MS = 15_000;

export function redeemRelayInvite(code, options = {}) {
  return request('POST', '/api/relay/invite/redeem', { code }, {
    timeoutMs: PLAN_INVITE_TIMEOUT_MS,
    ...options,
  });
}

export function describePlanInviteError(error) {
  return error?.message === 'invite code could not be redeemed'
    ? '无法兑换这个套餐邀请码，请核对后重试。'
    : error?.message || '兑换失败，请稍后重试。';
}
