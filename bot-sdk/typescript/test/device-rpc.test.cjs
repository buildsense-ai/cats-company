const test = require('node:test');
const assert = require('node:assert/strict');
const { once } = require('node:events');
const { WebSocketServer } = require('ws');
const { CatsBot, ProtocolError, RateLimitError } = require('../dist');

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

function ack(ws, msg, code = 200, text = 'ok', params = {}) {
  ws.send(JSON.stringify({
    ctrl: {
      id: msg.device_rpc.id,
      code,
      text,
      params,
    },
  }));
}

function latestDeviceRPC(messages) {
  const msg = messages.findLast((item) => item.device_rpc);
  assert.ok(msg, 'expected a device_rpc envelope');
  return msg.device_rpc;
}

function onceBot(bot, event) {
  return new Promise((resolve) => {
    bot.once(event, (...args) => resolve(args));
  });
}

test('sendDeviceRPC sends a top-level device_rpc envelope', async () => {
  await withBot((ws, msg) => ack(ws, msg, 200, 'ok', { request_id: 'rpc-raw' }), async ({ bot, messages }) => {
    const params = await bot.sendDeviceRPC({
      type: 'request',
      request_id: 'rpc-raw',
      grant_id: 'grant-1',
      operation: 'read_file',
      payload: { path: 'notes.txt' },
    });

    assert.equal(params.request_id, 'rpc-raw');
    const rpc = latestDeviceRPC(messages);
    assert.equal(rpc.type, 'request');
    assert.equal(rpc.request_id, 'rpc-raw');
    assert.equal(rpc.grant_id, 'grant-1');
    assert.equal(rpc.operation, 'read_file');
    assert.deepEqual(rpc.payload, { path: 'notes.txt' });
    assert.equal(typeof rpc.id, 'string');
  });
});

test('sendDeviceRPCRequest generates a request_id and preserves request fields', async () => {
  await withBot((ws, msg) => ack(ws, msg, 200, 'ok', {
    request_id: msg.device_rpc.request_id,
    device_id: 'alice-laptop',
    expires_at: 1893456000,
  }), async ({ bot, messages }) => {
    const requestAck = await bot.sendDeviceRPCRequest({
      grant_id: 'grant-2',
      operation: 'grep',
      tool_name: 'grep',
      payload: { pattern: 'invoice' },
      session_key: 'session-1',
      topic_id: 'p2p_7_43',
      topic_type: 'p2p',
      actor_user_id: 'usr100',
      owner_user_id: 'usr7',
      identity_source: 'channel_identity_link',
      agent_id: 'usr43',
      agent_body_id: 'body-agent',
      device_id: 'alice-laptop',
      device_body_id: 'body-alice-laptop',
      device_installation_id: 'install-alice-laptop',
    });

    const requestID = requestAck.request_id;
    assert.match(requestID, /^rpc_\d+_\d+$/);
    assert.equal(requestAck.device_id, 'alice-laptop');
    assert.equal(requestAck.expires_at, 1893456000);
    const rpc = latestDeviceRPC(messages);
    assert.equal(rpc.type, 'request');
    assert.equal(rpc.request_id, requestID);
    assert.equal(rpc.grant_id, 'grant-2');
    assert.equal(rpc.operation, 'grep');
    assert.equal(rpc.tool_name, 'grep');
    assert.equal(rpc.session_key, 'session-1');
    assert.equal(rpc.topic_id, 'p2p_7_43');
    assert.equal(rpc.topic_type, 'p2p');
    assert.equal(rpc.actor_user_id, 'usr100');
    assert.equal(rpc.owner_user_id, 'usr7');
    assert.equal(rpc.identity_source, 'channel_identity_link');
    assert.equal(rpc.agent_id, 'usr43');
    assert.equal(rpc.agent_body_id, 'body-agent');
    assert.equal(rpc.device_id, 'alice-laptop');
    assert.equal(rpc.device_body_id, 'body-alice-laptop');
    assert.equal(rpc.device_installation_id, 'install-alice-laptop');
    assert.deepEqual(rpc.payload, { pattern: 'invoice' });
  });
});

test('sendDeviceRPCRequest preserves file-level write and edit operations used by device RPC agents', async () => {
  await withBot((ws, msg) => ack(ws, msg, 200, 'ok', {
    request_id: msg.device_rpc.request_id,
    operation: msg.device_rpc.operation,
    device_id: 'alice-laptop',
    device_body_id: 'body-alice-laptop',
    device_installation_id: 'install-alice-laptop',
  }), async ({ bot, messages }) => {
    const writeAck = await bot.sendDeviceRPCRequest({
      grant_id: 'grant-write',
      operation: 'write_file',
      tool_name: 'write_file',
      payload: { path: 'quote.xlsx', content: 'updated' },
      session_key: 'session:v2:catscompany:p2p:p2p_7_43:agent:usr43',
      topic_id: 'p2p_7_43',
      topic_type: 'p2p',
    });
    const editAck = await bot.sendDeviceRPCRequest({
      grant_id: 'grant-edit',
      operation: 'edit_file',
      tool_name: 'edit_file',
      payload: { path: 'quote.xlsx', old_string: 'before', new_string: 'after' },
      session_key: 'session:v2:catscompany:p2p:p2p_7_43:agent:usr43',
      topic_id: 'p2p_7_43',
      topic_type: 'p2p',
    });

    assert.equal(writeAck.operation, 'write_file');
    assert.equal(editAck.operation, 'edit_file');
    assert.equal(writeAck.device_id, 'alice-laptop');
    assert.equal(editAck.device_body_id, 'body-alice-laptop');

    const envelopes = messages.filter((item) => item.device_rpc).map((item) => item.device_rpc);
    assert.equal(envelopes.at(-2).operation, 'write_file');
    assert.equal(envelopes.at(-2).tool_name, 'write_file');
    assert.deepEqual(envelopes.at(-2).payload, { path: 'quote.xlsx', content: 'updated' });
    assert.equal(envelopes.at(-1).operation, 'edit_file');
    assert.equal(envelopes.at(-1).tool_name, 'edit_file');
    assert.deepEqual(envelopes.at(-1).payload, { path: 'quote.xlsx', old_string: 'before', new_string: 'after' });
  });
});

test('sendDeviceRPCResult sends result and error payloads', async () => {
  await withBot((ws, msg) => ack(ws, msg), async ({ bot, messages }) => {
    await bot.sendDeviceRPCResult({
      request_id: 'rpc-result',
      grant_id: 'grant-result',
      actor_user_id: 'usr100',
      owner_user_id: 'usr7',
      identity_source: 'channel_identity_link',
      device_id: 'alice-laptop',
      operation: 'read_file',
      tool_name: 'read_file',
      result: { ok: true },
    });
    await bot.sendDeviceRPCResult({
      request_id: 'rpc-error',
      error: { code: 'read_failed', message: 'cannot read file' },
    });

    const envelopes = messages.filter((item) => item.device_rpc).map((item) => item.device_rpc);
    assert.deepEqual(envelopes[0], {
      id: envelopes[0].id,
      type: 'result',
      request_id: 'rpc-result',
      grant_id: 'grant-result',
      actor_user_id: 'usr100',
      owner_user_id: 'usr7',
      identity_source: 'channel_identity_link',
      device_id: 'alice-laptop',
      operation: 'read_file',
      tool_name: 'read_file',
      result: { ok: true },
    });
    assert.deepEqual(envelopes[1], {
      id: envelopes[1].id,
      type: 'result',
      request_id: 'rpc-error',
      error: { code: 'read_failed', message: 'cannot read file' },
    });
  });
});

test('server device_rpc envelopes emit the device_rpc event', async () => {
  await withBot((ws, msg) => ack(ws, msg), async ({ bot, getSocket }) => {
    const received = onceBot(bot, 'device_rpc');
    getSocket().send(JSON.stringify({
      device_rpc: {
        type: 'result',
        request_id: 'rpc-from-server',
        result: { text: 'done' },
      },
    }));

    const [rpc] = await received;
    assert.equal(rpc.type, 'result');
    assert.equal(rpc.request_id, 'rpc-from-server');
    assert.deepEqual(rpc.result, { text: 'done' });
  });
});

test('message context exposes CatsCo identity device grants', async () => {
  await withBot((ws, msg) => ack(ws, msg), async ({ bot, getSocket }) => {
    const received = onceBot(bot, 'message');
    getSocket().send(JSON.stringify({
      data: {
        topic: 'p2p_7_43',
        from: 'usr7',
        seq: 1,
        content: '查一下本地报价单',
        metadata: {
          catsco_identity: {
            device_grants: [{
              kind: 'scoped_device_grant',
              source: 'server_canonical_message',
              grantId: 'grant-ctx',
              status: 'active',
              identityTrust: 'server_canonical',
              deviceId: 'alice-laptop',
              ownerUserId: 'usr7',
              sessionKey: 'p2p_7_43',
              topicId: 'p2p_7_43',
              topicType: 'p2p',
              actorUserId: 'usr7',
              operations: ['read_file'],
              createdAt: 1893455000,
              expiresAt: 1893456000,
            }],
            device_selection: {
              kind: 'user_device_selection',
              source: 'server_canonical_message',
              schemaVersion: 1,
              status: 'selected',
              sessionKey: 'p2p_7_43',
              topicId: 'p2p_7_43',
              topicType: 'p2p',
              actorUserId: 'usr7',
              selectedDevice: { deviceId: 'alice-laptop', operations: ['read_file'] },
              createdAt: 1893455000,
            },
          },
        },
      },
    }));

    const [ctx] = await received;
    assert.equal(ctx.metadata.catsco_identity.device_grants[0].grantId, 'grant-ctx');
    assert.equal(ctx.deviceGrants[0].grantId, 'grant-ctx');
    assert.equal(ctx.deviceSelection.selectedDevice.deviceId, 'alice-laptop');
  });
});

test('device_rpc ack errors reject with SDK errors', async () => {
  await withBot((ws, msg) => ack(ws, msg, 403, 'denied'), async ({ bot }) => {
    await assert.rejects(
      bot.sendDeviceRPC({ type: 'request', request_id: 'rpc-denied', grant_id: 'grant-3', operation: 'read_file' }),
      ProtocolError,
    );
  });

  await withBot((ws, msg) => ack(ws, msg, 429, 'too many requests'), async ({ bot }) => {
    await assert.rejects(
      bot.sendDeviceRPC({ type: 'request', request_id: 'rpc-rate', grant_id: 'grant-4', operation: 'read_file' }),
      RateLimitError,
    );
  });
});
