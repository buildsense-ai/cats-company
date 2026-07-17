const test = require('node:test');
const assert = require('node:assert/strict');
const { once } = require('node:events');
const { WebSocketServer } = require('ws');
const { CatsBot, ProtocolError } = require('../dist');

async function withBot(onEnvelope, run) {
  const wss = new WebSocketServer({ port: 0 });
  await once(wss, 'listening');
  const messages = [];
  let socket;

  wss.on('connection', (ws) => {
    socket = ws;
    ws.on('message', (raw) => {
      const msg = JSON.parse(raw.toString());
      messages.push(msg);
      if (msg.hi) {
        ws.send(JSON.stringify({
          ctrl: {
            id: msg.hi.id,
            code: 200,
            params: { build: 'catscompany', uid: 'usr42', name: 'sdk-bot' },
          },
        }));
        return;
      }
      onEnvelope?.(ws, msg);
    });
  });

  const { port } = wss.address();
  const bot = new CatsBot({
    serverUrl: `ws://127.0.0.1:${port}`,
    apiKey: 'cc_test_key',
    bodyId: 'body-sdk-test',
    httpBaseUrl: 'http://127.0.0.1:9',
  });

  try {
    await bot.connect();
    await run({ bot, messages, getSocket: () => socket });
  } finally {
    bot.disconnect();
    await new Promise((resolve) => wss.close(resolve));
  }
}

test('sendTaskStatus sends task_status and returns the acknowledged status', async () => {
  const acknowledged = {
    topic_id: 'p2p_7_42',
    run_id: 'run-1',
    state: 'running',
    summary: 'building',
    source_uid: 42,
    updated_at: '2026-07-17T03:00:00Z',
    expires_at: '2026-07-17T09:00:00Z',
  };

  await withBot((ws, msg) => {
    if (!msg.pub) return;
    ws.send(JSON.stringify({
      ctrl: {
        id: msg.pub.id,
        topic: msg.pub.topic,
        code: 200,
        text: 'ok',
        params: { seq: 0, task_status: acknowledged },
      },
    }));
  }, async ({ bot, messages }) => {
    const status = await bot.sendTaskStatus('p2p_7_42', {
      run_id: 'run-1',
      state: 'running',
      summary: 'building',
    });

    assert.deepEqual(status, acknowledged);
    const pub = messages.find((message) => message.pub)?.pub;
    assert.ok(pub, 'expected a pub envelope');
    assert.equal(pub.topic, 'p2p_7_42');
    assert.equal(pub.type, 'task_status');
    assert.deepEqual(pub.content, {
      run_id: 'run-1',
      state: 'running',
      summary: 'building',
    });
  });
});

test('sendTaskStatus rejects an acknowledgement without task_status', async () => {
  await withBot((ws, msg) => {
    if (!msg.pub) return;
    ws.send(JSON.stringify({
      ctrl: { id: msg.pub.id, topic: msg.pub.topic, code: 200, text: 'ok', params: { seq: 0 } },
    }));
  }, async ({ bot }) => {
    await assert.rejects(
      bot.sendTaskStatus('p2p_7_42', { state: 'completed' }),
      (error) => error instanceof ProtocolError && error.message.includes('missing status payload'),
    );
  });
});

test('task_status server envelopes emit the typed event', async () => {
  await withBot(undefined, async ({ bot, getSocket }) => {
    const status = {
      topic_id: 'p2p_7_42',
      run_id: 'run-1',
      state: 'completed',
      updated_at: '2026-07-17T03:10:00Z',
    };
    const received = new Promise((resolve) => bot.once('task_status', resolve));
    getSocket().send(JSON.stringify({ task_status: status }));
    assert.deepEqual(await received, status);
  });
});
