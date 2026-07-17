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

process.env.BOT_API_KEY = 'test-key';
process.env.BOT_BODY_ID = 'test-body';
process.env.LLM_API_KEY = 'test-llm-key';
require('../dist/main.js');

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const successResponse = {
  ok: true,
  json: async () => ({ choices: [{ message: { content: 'reply' } }] }),
};

function context(topic, sendTaskStatus) {
  return {
    from: 'usr42',
    topic,
    text: 'hello',
    bot: { sendTaskStatus },
    withTyping: async (task) => task(),
    reply: async () => {},
  };
}

test('unacknowledged running status does not delay LLM execution', async () => {
  let fetchStarted = false;
  global.fetch = async () => {
    fetchStarted = true;
    return successResponse;
  };

  await Promise.race([
    messageHandler(context('p2p_7_42', async (_topic, status) => {
      if (status.state === 'running') {
        return new Promise(() => {});
      }
    })),
    sleep(150).then(() => {
      throw new Error('running status delayed LLM execution');
    }),
  ]);

  assert.equal(fetchStarted, true);
});

test('unacknowledged terminal status does not block the next topic task', async () => {
  let fetchCount = 0;
  let blockedRunID = '';
  global.fetch = async () => {
    fetchCount++;
    return successResponse;
  };

  const sendTaskStatus = async (_topic, status) => {
    if (status.state === 'running' && !blockedRunID) {
      blockedRunID = status.run_id;
    }
    if (status.state === 'completed' && status.run_id === blockedRunID) {
      return new Promise(() => {});
    }
  };
  const nextContext = context('p2p_7_99', sendTaskStatus);

  await messageHandler(nextContext);
  await Promise.race([
    messageHandler(nextContext),
    sleep(150).then(() => {
      throw new Error('terminal status blocked the next topic task');
    }),
  ]);

  assert.equal(fetchCount, 2);
});
