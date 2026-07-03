import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import { URL } from 'node:url';

const port = Number(process.env.MOCK_CATS_PORT || 6061);
const scenario = String(process.env.MOCK_CATS_SCENARIO || 'new').trim().toLowerCase();
const echoReplies = ['1', 'true', 'yes', 'on'].includes(String(process.env.MOCK_CATS_ECHO || '').trim().toLowerCase());
const tutorialTasksFile = String(process.env.MOCK_CATS_TUTORIAL_TASKS_FILE || '').trim();
const tutorialTasksJSON = String(process.env.MOCK_CATS_TUTORIAL_TASKS_JSON || '').trim();

let nextUserId = 100;
let nextBotId = 200;
const users = new Map();
const tokens = new Map();
const sessions = new Map();
const botsByOwner = new Map();
const relayKeysByUserId = new Map();
const onlineBodies = new Map();
const agentSockets = new Map();
const webSocketsByUserId = new Map();
const messagesByTopic = new Map();
let nextSeq = 1;

function p2pTopicId(uid1, uid2) {
  const a = Number(uid1);
  const b = Number(uid2);
  const [left, right] = a < b ? [a, b] : [b, a];
  return `p2p_${left}_${right}`;
}

function seedExistingBot(user) {
  if (scenario !== 'existing') return;
  const existing = {
    id: nextBotId++,
    uid: nextBotId - 1,
    username: `existing_bot_${user.id}`,
    display_name: 'Existing Local Bot',
    avatar_url: '',
    api_key: `mock-api-key-${nextBotId - 1}`,
    owner_id: user.id,
  };
  botsByOwner.set(user.id, [existing]);
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

function getBearerUser(req) {
  const auth = String(req.headers.authorization || '');
  const token = auth.replace(/^Bearer\s+/i, '').trim();
  return tokens.get(token);
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
      return send(res, 200, { ok: true, mode: 'local-onboarding-mock', scenario });
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
      nextSeq = 1;
      nextUserId = 100;
      nextBotId = 200;
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
          is_online: onlineBodies.has(bot.api_key),
          topic_id: p2pTopicId(user.id, bot.id),
        })),
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
      return send(res, 200, { conversations: [] });
    }

    if (req.method === 'GET' && url.pathname === '/api/messages') {
      const topicId = String(url.searchParams.get('topic_id') || url.searchParams.get('topic') || '').trim();
      const limit = Number(url.searchParams.get('limit') || 50);
      const list = topicId ? (messagesByTopic.get(topicId) || []) : [...messagesByTopic.values()].flat();
      return send(res, 200, { messages: list.slice(-limit) });
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
  const webUser = token ? tokens.get(token) : null;
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
          from: bot?.id || 0,
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
            from: bot?.id || 0,
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
server.listen(port, '127.0.0.1', () => {
  console.log(`[mock] CatsCo local onboarding mock server listening on http://localhost:${port}`);
  console.log(`[mock] scenario=${scenario} (set MOCK_CATS_SCENARIO=new|existing)`);
  console.log(`[mock] echoReplies=${echoReplies} (set MOCK_CATS_ECHO=1 to echo without a real model)`);
});
