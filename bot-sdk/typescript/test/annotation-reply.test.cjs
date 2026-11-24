// sendAnnotationReply tests: the point-back metadata is normalized against
// the catsco.gateway-annotation-reply.v1 bounds before it rides the pub
// envelope, and invalid input fails locally without sending.

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

const validReply = {
  app_id: 'board',
  target: { element_id: 'submit-btn', selector: 'button#submit' },
  note: '改这里',
};

test('sendAnnotationReply attaches the normalized reply metadata to pub', async () => {
  await withBot((ws, msg) => {
    if (!msg.pub) return;
    ws.send(JSON.stringify({ ctrl: { id: msg.pub.id, topic: msg.pub.topic, code: 200, text: 'ok', params: { seq: 7 } } }));
  }, async ({ bot, messages }) => {
    const seq = await bot.sendAnnotationReply('p2p_7_42', '我已经标出来了', validReply);
    assert.equal(seq, 7);
    const pub = messages.find((message) => message.pub)?.pub;
    assert.equal(pub.metadata.gateway_annotation_reply.contract_version, 'catsco.gateway-annotation-reply.v1');
    assert.equal(pub.metadata.gateway_annotation_reply.app_id, 'board');
    assert.deepEqual(pub.metadata.gateway_annotation_reply.target, {
      element_id: 'submit-btn', selector: 'button#submit',
    });
    assert.equal(pub.metadata.gateway_annotation_reply.note, '改这里');
    assert.equal(pub.content, '我已经标出来了');
  });
});

test('sendAnnotationReply accepts a region rect and stamps coordinate_space', async () => {
  await withBot((ws, msg) => {
    if (!msg.pub) return;
    ws.send(JSON.stringify({ ctrl: { id: msg.pub.id, topic: msg.pub.topic, code: 200, text: 'ok', params: { seq: 1 } } }));
  }, async ({ bot, messages }) => {
    await bot.sendAnnotationReply('p2p_7_42', '看这一片', {
      app_id: 'board',
      target: { rect: { x: 0.1, y: 0.2, width: 0.3, height: 0.4 } },
    });
    const pub = messages.find((message) => message.pub)?.pub;
    assert.deepEqual(pub.metadata.gateway_annotation_reply.target, {
      rect: { x: 0.1, y: 0.2, width: 0.3, height: 0.4 },
      coordinate_space: 'viewport',
    });
  });
});

test('sendAnnotationReply rejects malformed replies before sending', async () => {
  await withBot(null, async ({ bot, messages }) => {
    const bad = [
      { app_id: 'BAD', target: { element_id: 'x' } },
      { app_id: 'board', target: {} },
      { app_id: 'board', target: { element_id: 'x\ny' } },
      { app_id: 'board', target: { rect: { x: 0.9, y: 0.1, width: 0.5, height: 0.2 } } },
      { app_id: 'board', target: { rect: { x: 0.1, y: 0.1, width: 0, height: 0.2 } } },
      null,
    ];
    for (const reply of bad) {
      await assert.rejects(() => bot.sendAnnotationReply('p2p_7_42', 'x', reply), ProtocolError);
    }
    assert.equal(messages.filter((message) => message.pub).length, 0);
  });
});

test('sendMessage accepts a metadata option without breaking replyTo', async () => {
  await withBot((ws, msg) => {
    if (!msg.pub) return;
    ws.send(JSON.stringify({ ctrl: { id: msg.pub.id, topic: msg.pub.topic, code: 200, text: 'ok', params: { seq: 3 } } }));
  }, async ({ bot, messages }) => {
    await bot.sendMessage('p2p_7_42', 'plain', 9);
    const plain = messages.find((message) => message.pub)?.pub;
    assert.equal(plain.reply_to, 9);
    assert.equal(plain.metadata, undefined);

    await bot.sendMessage('p2p_7_42', 'meta', { replyTo: 10, metadata: { custom: true } });
    const withMeta = messages.filter((message) => message.pub)[1].pub;
    assert.equal(withMeta.reply_to, 10);
    assert.deepEqual(withMeta.metadata, { custom: true });
  });
});
