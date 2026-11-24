// @catscompany/bot-sdk — barrel export

export { CatsBot } from './bot';
export { MessageContext } from './context';
export type { TypingHeartbeatOptions } from './context';
export { FileUploader } from './uploader';

export {
  parseTopic,
  buildP2PTopic,
  uidToNumber,
  numberToUid,
  type TopicInfo,
} from './topic';

export {
  CatsBotError,
  ConnectionError,
  HandshakeError,
  ProtocolError,
  RateLimitError,
  UploadError,
} from './errors';

export {
  GATEWAY_ANNOTATION_REPLY_CONTRACT,
  GATEWAY_ANNOTATION_REPLY_METADATA_KEY,
} from './types';

export type {
  // Client messages
  MsgClientHi,
  MsgClientAcc,
  MsgClientLogin,
  MsgClientSub,
  MsgClientPub,
  MsgClientGet,
  MsgClientSet,
  MsgClientDel,
  MsgClientNote,
  MsgClientFriend,
  MsgDeviceRPC,
  MsgDeviceRPCError,
  DeviceRPCType,
  DeviceRPCOperation,
  DeviceRPCAckParams,
  DeviceRPCRequestAck,
  DeviceRPCRequestInput,
  DeviceRPCResultInput,
  ScopedDeviceGrant,
  DeviceSelectionStatus,
  DeviceSelectionDevice,
  DeviceSelectionCandidate,
  DeviceSelection,
  CatsCoIdentityMetadata,
  ClientMessage,
  // Server messages
  MsgServerCtrl,
  MsgServerData,
  MsgServerPres,
  MsgServerMeta,
  MsgServerInfo,
  MsgServerFriend,
  ConversationTaskStatusState,
  ConversationTaskStatus,
  ConversationTaskStatusInput,
  ServerMessage,
  // Rich content
  RichContentImage,
  RichContentFile,
  RichContentLinkPreview,
  RichContentCard,
  RichContent,
  MessageContent,
  // Gateway annotation point-back
  GatewayAnnotationReply,
  GatewayAnnotationReplyTarget,
  GatewayAnnotationAgentContext,
  // Upload
  UploadResult,
  // Config & events
  CatsBotConfig,
  BotEventMap,
} from './types';
