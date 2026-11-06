import { describe, test } from 'node:test';
import * as assert from 'node:assert';
import { createHmac } from 'node:crypto';

import {
  extractContentText,
  extractStructuredContent,
  mintUserToken,
  parseArgs,
  parseSseEvents,
} from './verify-agent-tools.mjs';

describe('parseSseEvents', () => {
  test('按空行切事件，多行 data 拼起来再解析', () => {
    const body = [
      'event: message',
      'data: {"jsonrpc":"2.0","id":1,',
      'data: "result":{"tools":[]}}',
      '',
      'data: {"jsonrpc":"2.0","id":2,"result":{}}',
      '',
    ].join('\n');
    const events = parseSseEvents(body);
    assert.equal(events.length, 2);
    assert.deepEqual(events[0].result, { tools: [] });
    assert.equal(events[1].id, 2);
  });

  test('CRLF、注释行、空 data 都不算事件', () => {
    const body = ': keep-alive\r\n\r\ndata:\r\n\r\ndata: {"a":1}\r\n\r\n';
    const events = parseSseEvents(body);
    assert.equal(events.length, 1);
    assert.deepEqual(events[0], { a: 1 });
  });

  test('半截 JSON 留痕而不是静默丢掉（以前这种被当成「空工具面」）', () => {
    const events = parseSseEvents('data: {"jsonrpc":"2.0","id":1,"res\n\n');
    assert.equal(events.length, 1);
    assert.ok(events[0].__unparsed);
  });

  test('空体 / 非字符串返回空数组', () => {
    assert.deepEqual(parseSseEvents(''), []);
    assert.deepEqual(parseSseEvents(undefined), []);
  });
});

describe('mintUserToken', () => {
  test('HS256 三段的签名可复算，claims 与平台一致', () => {
    const secret = 'test-secret';
    const token = mintUserToken(secret, 1619, { ttlSeconds: 60 });
    const [header, claims, sig] = token.split('.');
    assert.equal(token.split('.').length, 3);
    const expected = createHmac('sha256', secret)
      .update(`${header}.${claims}`)
      .digest('base64')
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
    assert.equal(sig, expected);

    const decoded = JSON.parse(Buffer.from(claims, 'base64').toString('utf8'));
    assert.equal(decoded.token_type, 'user');
    assert.equal(decoded.userId, 1619);
    assert.equal(decoded.iss, 'catscompany');
    assert.ok(decoded.exp > decoded.iat);
  });

  test('没有 secret 直接报错，不要发一个空签名出去', () => {
    assert.throws(() => mintUserToken('', 1), /JWT secret/);
  });
});

describe('从响应里取结果', () => {
  const events = [{
    result: {
      content: [{ type: 'text', text: 'run=x status=completed output=OK' }],
      structuredContent: { run_id: 'run_x', status: 'completed', output: { text: 'OK' } },
    },
  }];

  test('正文在 structuredContent，不是 content[0].text', () => {
    assert.equal(extractStructuredContent(events).output.text, 'OK');
    assert.match(extractContentText(events), /status=completed/);
  });

  test('拿不到时返回 undefined / 空串', () => {
    assert.equal(extractStructuredContent([{ result: {} }]), undefined);
    assert.equal(extractContentText([{ result: {} }]), '');
  });
});

describe('parseArgs', () => {
  test('常规用法与 = 写法都能解析', () => {
    const args = parseArgs(['--uid', '1619', '--call=catsco_demo', '--text', '只回复 OK', '--json']);
    assert.equal(args.uid, 1619);
    assert.equal(args.call, 'catsco_demo');
    assert.equal(args.text, '只回复 OK');
    assert.equal(args.json, true);
  });

  test('缺值 / 未知参数要报错，不能默默变成默认行为', () => {
    assert.throws(() => parseArgs(['--uid']), /需要一个值/);
    assert.throws(() => parseArgs(['--nope', '1']), /未知参数/);
  });
});
