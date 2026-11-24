// Point-back end-to-end on the bot side: the LLM may emit an
// [[ANNOTATION_REPLY]] marker naming one annotation index; the bot strips
// the marker, echoes the server-validated target from the annotation
// context (never model output), and sends via replyWithAnnotation.

const assert = require('node:assert/strict');
const test = require('node:test');
const Module = require('node:module');

let messageHandler;
const originalLoad = Module._load;

Module._load = function load(request, parent, isMain) {
  if (request === '@catscompany/bot-sdk') {
    return {
      CatsBot: class {
        on(event, callback) {
          if (event === 'message') {
            messageHandler = callback;
          }
          return this;
        }

        run() {
          return new Promise(() => {});
        }
      },
    };
  }
  return originalLoad.call(this, request, parent, isMain);
};

process.env.BOT_API_KEY = 'cc_test_key';
process.env.BOT_BODY_ID = 'test-body';
process.env.LLM_API_KEY = 'test-key';
require('../dist/main.js');

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function llmReply(content) {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({ choices: [{ message: { content } }] }),
  });
}

function context({ replyWithAnnotation, gatewayAnnotations }) {
  return {
    from: 'usr42',
    topic: 'p2p_7_42',
    text: 'hello',
    gatewayAnnotations,
    bot: { sendTaskStatus: async () => {} },
    withTyping: async (task) => task(),
    reply: async () => {},
    replyWithAnnotation,
  };
}

const annotationContext = {
  schema: 'catsco.gateway_annotations_context.v1',
  app_id: 'board',
  agent_uid: '42',
  page: { path: '/board', revision: 'r7' },
  annotations: [
    { id: 'a1', index: 1, kind: 'element', label: 'button "提交"', body: '改成蓝色',
      target: { element_id: 'submit-btn', selector: 'button#submit' } },
  ],
};

test('a reply marker echoes the annotated target, not model output', async () => {
  llmReply('我把那个按钮标出来了 [[ANNOTATION_REPLY]]{"annotation_index":1,"note":"看这里"}');
  let call = null;
  await messageHandler(context({
    gatewayAnnotations: annotationContext,
    replyWithAnnotation: async (text, pointBack) => { call = { text, pointBack }; },
  }));

  assert.ok(call, 'expected a replyWithAnnotation call');
  assert.equal(call.text, '我把那个按钮标出来了');
  assert.deepEqual(call.pointBack, {
    app_id: 'board',
    target: { element_id: 'submit-btn', selector: 'button#submit' },
    note: '看这里',
  });
});

test('a forged annotation_index or a marker without context falls back to a plain reply', async () => {
  llmReply('hi [[ANNOTATION_REPLY]]{"annotation_index":9,"note":"x"}');
  let pointed = null;
  let plain = '';
  const ctx = context({
    gatewayAnnotations: annotationContext,
    replyWithAnnotation: async (text, pointBack) => { pointed = { text, pointBack }; },
  });
  ctx.reply = async (text) => { plain = text; };
  await messageHandler(ctx);
  assert.equal(pointed, null);
  assert.equal(plain, 'hi');

  // Marker with no annotation context at all stays a plain reply too.
  llmReply('hi [[ANNOTATION_REPLY]]{"annotation_index":1}');
  plain = '';
  const bare = context({ replyWithAnnotation: async () => { pointed = true; } });
  bare.reply = async (text) => { plain = text; };
  pointed = null;
  await messageHandler(bare);
  assert.equal(pointed, null);
  assert.equal(plain, 'hi');
});

test('the marker is stripped from conversation history so it is not re-learned', async () => {
  llmReply('done [[ANNOTATION_REPLY]]{"annotation_index":1}');
  await messageHandler(context({
    gatewayAnnotations: annotationContext,
    replyWithAnnotation: async () => {},
  }));
  // Second turn: inspect the assistant turn the model receives. The
  // marker legitimately appears in the system prompt instructions; what
  // must be stripped is the stored assistant message.
  let sent = '';
  global.fetch = async (_url, init) => {
    sent = init.body;
    return { ok: true, json: async () => ({ choices: [{ message: { content: 'ok' } }] }) };
  };
  await messageHandler(context({
    gatewayAnnotations: annotationContext,
    replyWithAnnotation: async () => {},
  }));
  const messages = JSON.parse(sent).messages;
  const assistantTurns = messages.filter((m) => m.role === 'assistant');
  assert.equal(assistantTurns.some((m) => m.content.includes('[[ANNOTATION_REPLY]]')), false);
});
