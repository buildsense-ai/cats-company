import {
  CatsBot,
  type BotEventMap,
  type DeviceRPCRequestAck,
  type DeviceRPCRequestInput,
  type MsgDeviceRPC,
  type ScopedDeviceGrant,
} from '../../dist';

const bot = new CatsBot({
  serverUrl: 'ws://localhost:6061/v0/channels',
  apiKey: 'cc_test',
  bodyId: 'body-test',
});

const onDeviceRPC: BotEventMap['device_rpc'] = (msg: MsgDeviceRPC) => {
  if (msg.type === 'result' && msg.error) {
    const code: string = msg.error.code;
    void code;
  }
};

bot.on('device_rpc', onDeviceRPC);

const input: DeviceRPCRequestInput = {
  grant_id: 'grant-1',
  operation: 'read_file',
  payload: { path: 'quote.xlsx' },
};
const writeInput: DeviceRPCRequestInput = {
  grant_id: 'grant-write',
  operation: 'write_file',
  payload: { path: 'quote.xlsx', content: 'updated' },
};
const editInput: DeviceRPCRequestInput = {
  grant_id: 'grant-edit',
  operation: 'edit_file',
  owner_user_id: 'usr7',
  identity_source: 'metadata.catsco_identity',
  payload: { path: 'quote.xlsx', old_string: 'old', new_string: 'new' },
};

void bot.sendDeviceRPCRequest(input).then((ack: DeviceRPCRequestAck) => ack.request_id);
void bot.sendDeviceRPCRequest(writeInput).then((ack: DeviceRPCRequestAck) => ack.request_id);
void bot.sendDeviceRPCRequest(editInput).then((ack: DeviceRPCRequestAck) => ack.request_id);
void bot.sendDeviceRPC({
  type: 'result',
  request_id: 'rpc-1',
  owner_user_id: 'usr7',
  identity_source: 'metadata.catsco_identity',
  result: { ok: true },
});

bot.on('message', (ctx) => {
  const grants: ScopedDeviceGrant[] = ctx.deviceGrants;
  const grantID: string | undefined = grants[0]?.grantId;
  const selectionStatus = ctx.deviceSelection?.status;
  void grantID;
  void selectionStatus;
});
