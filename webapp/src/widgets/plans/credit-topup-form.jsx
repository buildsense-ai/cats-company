import React, { useState } from 'react';
import './credit-commerce.css';

export function parseTopupAmount(value) {
  if (!/^\d+(?:\.\d{1,2})?$/.test(String(value).trim())) return null;
  const [whole, decimal = ''] = String(value).trim().split('.');
  const fen = Number(whole) * 100 + Number(decimal.padEnd(2, '0'));
  return Number.isSafeInteger(fen) ? fen : null;
}

export default function CreditTopupForm({ config, disabled = false, onSelect }) {
  const [amount, setAmount] = useState('50');
  if (config?.enabled !== true) return null;
  const fen = parseTopupAmount(amount);
  const minimum = Number(config.min_amount_fen);
  const maximum = Number(config.max_amount_fen);
  const rate = Number(config.credits_per_cny);
  const valid = Number.isSafeInteger(fen) && minimum > 0 && maximum >= minimum
    && rate > 0 && fen >= minimum && fen <= maximum;
  const credits = valid ? (fen / 100 * rate).toLocaleString('zh-CN', { maximumFractionDigits: 2 }) : '—';
  return (
    <form className="credit-topup-card" aria-label="充值点数" onSubmit={event => {
      event.preventDefault();
      if (valid && !disabled) onSelect({
        id: `credit_topup:${fen}`, kind: 'credit_topup', amount_fen: fen,
        price_fen: fen, name: `充值 ${credits} 点`,
      });
    }}>
      <div className="credit-topup-heading"><strong>给工作补充燃油</strong><span>点数充值</span></div>
      <p>点数可用于模型消耗和机器升配。充值不附带机器，不改变套餐周期；套餐另含机器权益和周期点数。</p>
      <div className="credit-topup-presets" role="group" aria-label="快捷充值金额">
        {(config.presets_fen || []).filter(value => value >= minimum && value <= maximum).map(value => (
          <button key={value} type="button" className={fen === value ? 'active' : ''}
            aria-pressed={fen === value} disabled={disabled} onClick={() => setAmount(String(value / 100))}>¥{value / 100}</button>
        ))}
      </div>
      <label className="credit-topup-amount">自定义金额（元）
        <input inputMode="decimal" autoComplete="off" value={amount} disabled={disabled}
          onChange={event => setAmount(event.target.value)} aria-describedby="credit-topup-amount-help" />
      </label>
      <div id="credit-topup-amount-help" className="credit-topup-note">
        ¥{minimum / 100}～{maximum / 100}，最多两位小数 · ¥1＝{rate} 点
      </div>
      <div className="credit-topup-footer"><strong>{credits} 点</strong>
        <button type="submit" disabled={disabled || !valid}>支付宝充值</button>
      </div>
      <small>充值进入付费余额；如有欠款会先抵扣欠款。月额度仍按原周期刷新。</small>
    </form>
  );
}
