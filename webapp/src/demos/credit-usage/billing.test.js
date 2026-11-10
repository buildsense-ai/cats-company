import { it } from 'vitest';
import assert from 'node:assert/strict';
import { micros, calculateCall, consume, grant, canStart, survivors, nextCycle, proratedUpgrade } from './billing.mjs';
import { calls, people } from './data.mjs';
it('reconciles metering, allocation, repayment, task eligibility and cycle examples', () => {
assert.equal(calculateCall({input:1_000_000,cached:1_000_000,output:1_000_000},{input:micros(2),cached:micros(.2),output:micros(3)},9000),micros(4.68));
const s={monthly:micros(2),gift:micros(3),recharge:micros(4),debt:0};
const c=consume(s,micros(10),true);
assert.deepEqual(c.allocation,{monthly:micros(2),gift:micros(3),recharge:micros(4),debt:micros(1)});
assert.equal(canStart(c.after),false);
const gift=grant(c.after,'gift',micros(20));assert.equal(gift.after.debt,micros(1));assert.equal(gift.after.gift,micros(20));assert.equal(canStart(gift.after),false);
assert.throws(()=>consume(gift.after,1),/不能启动新任务/);
const topup=grant(gift.after,'recharge',micros(5));assert.equal(topup.debtPaid,micros(1));assert.equal(topup.after.recharge,micros(4));assert.equal(canStart(topup.after),true);assert.equal(canStart(topup.after,false),false);
const month=grant({...gift.after,monthly:micros(7)},'monthly',micros(60));assert.equal(month.after.monthly,micros(59));assert.equal(month.after.debt,0);
assert.deepEqual(survivors([{id:'a',state:'running',startedAt:10},{id:'b',state:'running',startedAt:20},{id:'c',state:'queued',startedAt:30}]).map(x=>x.id),['b']);
assert.equal(nextCycle('2027-01-31T10:00:00+08:00',31),'2027-02-28T02:00:00.000Z');
assert.equal(nextCycle('2027-02-28T10:00:00+08:00',31),'2027-03-31T02:00:00.000Z');
const up=proratedUpgrade({price:micros(99.9),quota:micros(105)},{price:micros(169.9),quota:micros(189)},'2026-11-01T10:00:00+08:00','2026-12-01T10:00:00+08:00','2026-11-16T10:00:00+08:00');assert.deepEqual(up,{price:micros(35),credits:micros(42)});
assert.equal(new Set(calls.map(x=>x.requestId)).size,calls.length);
for(const call of calls){assert(Number.isFinite(new Date(call.at).getTime()));if(call.status==='settled'){assert.equal(call.amount,Object.values(call.allocation).reduce((a,b)=>a+b,0));for(const key of ['monthly','gift','recharge'])assert.equal(call.before[key]-call.allocation[key],call.after[key]);assert.equal(call.before.debt+call.allocation.debt,call.after.debt);}}
assert(calls.some(c=>c.allocation?.debt>0));assert(calls.some(c=>c.status==='pending'));
assert.equal(people.length,5);


});
