export interface MsgClientHi {
    id?: string;
    ua?: string;
    ver?: string;
    lang?: string;
}
export interface MsgClientAcc {
    id?: string;
    user?: string;
    scheme?: string;
    secret?: string;
    desc?: Record<string, string>;
}
export interface MsgClientLogin {
    id?: string;
    scheme?: string;
    secret?: string;
}
export interface MsgClientSub {
    id?: string;
    topic: string;
}
export interface MsgClientPub {
    id?: string;
    topic: string;
    content: unknown;
    reply_to?: number;
}
export interface MsgClientGet {
    id?: string;
    topic: string;
    what?: string;
    seq?: number;
}
export interface MsgClientSet {
    id?: string;
    topic: string;
    desc?: unknown;
}
export interface MsgClientDel {
    id?: string;
    topic?: string;
    what?: string;
}
export interface MsgClientNote {
    topic: string;
    what: 'read' | 'recv' | 'kp';
    seq?: number;
}
export interface MsgClientFriend {
    id?: string;
    action: 'request' | 'accept' | 'reject' | 'block' | 'remove';
    user_id: number;
    msg?: string;
}
export type DeviceRPCType = 'request' | 'result';
export type DeviceRPCOperation = 'read_file' | 'glob' | 'grep' | 'write_file' | 'edit_file';
export interface MsgDeviceRPCError {
    code: string;
    message: string;
}
export interface MsgDeviceRPC {
    id?: string;
    type: DeviceRPCType;
    request_id: string;
    grant_id?: string;
    session_key?: string;
    topic_id?: string;
    topic_type?: string;
    actor_user_id?: string;
    owner_user_id?: string;
    identity_source?: string;
    agent_id?: string;
    agent_body_id?: string;
    device_id?: string;
    device_body_id?: string;
    device_installation_id?: string;
    operation?: DeviceRPCOperation;
    tool_name?: string;
    payload?: Record<string, unknown>;
    result?: unknown;
    error?: MsgDeviceRPCError;
    created_at?: number;
    expires_at?: number;
}
export interface DeviceRPCRequestInput {
    request_id?: string;
    grant_id: string;
    operation: DeviceRPCOperation;
    payload?: Record<string, unknown>;
    tool_name?: string;
    session_key?: string;
    topic_id?: string;
    topic_type?: string;
    actor_user_id?: string;
    owner_user_id?: string;
    identity_source?: string;
    agent_id?: string;
    agent_body_id?: string;
    device_id?: string;
    device_body_id?: string;
    device_installation_id?: string;
}
export interface DeviceRPCResultInput {
    request_id: string;
    grant_id?: string;
    session_key?: string;
    topic_id?: string;
    topic_type?: string;
    actor_user_id?: string;
    owner_user_id?: string;
    identity_source?: string;
    agent_id?: string;
    agent_body_id?: string;
    device_id?: string;
    device_body_id?: string;
    device_installation_id?: string;
    operation?: DeviceRPCOperation;
    tool_name?: string;
    result?: unknown;
    error?: MsgDeviceRPCError;
}
export interface DeviceRPCAckParams {
    request_id?: string;
    device_id?: string;
    device_body_id?: string;
    device_installation_id?: string;
    operation?: DeviceRPCOperation;
    tool_name?: string;
    expires_at?: number;
    [key: string]: unknown;
}
export interface DeviceRPCRequestAck extends DeviceRPCAckParams {
    request_id: string;
}
export interface ClientMessage {
    hi?: MsgClientHi;
    acc?: MsgClientAcc;
    login?: MsgClientLogin;
    sub?: MsgClientSub;
    pub?: MsgClientPub;
    get?: MsgClientGet;
    set?: MsgClientSet;
    del?: MsgClientDel;
    note?: MsgClientNote;
    friend?: MsgClientFriend;
    device_rpc?: MsgDeviceRPC;
}
export interface MsgServerCtrl {
    id?: string;
    topic?: string;
    code: number;
    text?: string;
    params?: Record<string, unknown>;
}
export interface MsgServerData {
    topic: string;
    from?: string;
    seq: number;
    content: unknown;
    type?: string;
    msg_type?: string;
    metadata?: Record<string, unknown>;
    content_blocks?: unknown[];
    mode?: string;
    role?: string;
    reply_to?: number;
}
export interface MsgServerPres {
    topic: string;
    what: 'on' | 'off' | 'msg' | 'upd';
    src?: string;
}
export interface MsgServerMeta {
    id?: string;
    topic: string;
    desc?: unknown;
    sub?: unknown;
}
export interface MsgServerInfo {
    topic: string;
    from: string;
    what: 'read' | 'recv' | 'kp';
    seq?: number;
}
export interface MsgServerFriend {
    action: 'request' | 'accepted' | 'rejected' | 'blocked' | 'removed';
    from: number;
    to: number;
    msg?: string;
}
export interface ScopedDeviceGrant {
    kind: string;
    source: string;
    grantId: string;
    status: string;
    identityTrust: string;
    identitySource?: string;
    deviceId: string;
    deviceDisplayName?: string;
    deviceBodyId?: string;
    deviceInstallationId?: string;
    ownerUserId: string;
    sessionKey: string;
    topicId: string;
    topicType: string;
    actorUserId: string;
    agentId?: string;
    agentBodyId?: string;
    operations: DeviceRPCOperation[];
    createdAt: number;
    expiresAt: number;
}
export type DeviceSelectionStatus = 'selected' | 'needs_selection' | 'unavailable';
export interface DeviceSelectionDevice {
    deviceId: string;
    displayName?: string;
    bodyId?: string;
    installationId?: string;
    operations?: DeviceRPCOperation[];
    lastSeenAt?: number;
}
export interface DeviceSelectionCandidate {
    deviceId: string;
    displayName?: string;
    operations?: DeviceRPCOperation[];
    lastSeenAt?: number;
}
export interface DeviceSelection {
    kind: 'user_device_selection' | string;
    source: string;
    schemaVersion: number;
    status: DeviceSelectionStatus;
    selectionSource?: string;
    sessionKey: string;
    topicId: string;
    topicType: string;
    actorUserId: string;
    agentId?: string;
    selectedDevice?: DeviceSelectionDevice;
    candidates?: DeviceSelectionCandidate[];
    candidateCount?: number;
    createdAt: number;
}
export interface CatsCoIdentityMetadata {
    schema_version?: number;
    actor?: Record<string, unknown>;
    topic?: Record<string, unknown>;
    permissions?: Record<string, unknown>;
    agent?: Record<string, unknown>;
    device_grants?: ScopedDeviceGrant[];
    device_selection?: DeviceSelection;
    [key: string]: unknown;
}
export interface ServerMessage {
    ctrl?: MsgServerCtrl;
    data?: MsgServerData;
    pres?: MsgServerPres;
    meta?: MsgServerMeta;
    info?: MsgServerInfo;
    friend?: MsgServerFriend;
    device_rpc?: MsgDeviceRPC;
}
export interface RichContentImage {
    type: 'image';
    payload: {
        url: string;
        width?: number;
        height?: number;
        name?: string;
        size?: number;
    };
}
export interface RichContentFile {
    type: 'file';
    payload: {
        url: string;
        name: string;
        size: number;
        mime_type?: string;
    };
}
export interface RichContentLinkPreview {
    type: 'link_preview';
    payload: {
        url: string;
        title?: string;
        description?: string;
        image_url?: string;
    };
}
export interface RichContentCard {
    type: 'card';
    payload: {
        title: string;
        description?: string;
        image_url?: string;
        actions?: Array<{
            label: string;
            url?: string;
            action?: string;
        }>;
    };
}
export type RichContent = RichContentImage | RichContentFile | RichContentLinkPreview | RichContentCard;
export type MessageContent = string | RichContent;
export interface UploadResult {
    file_key: string;
    url: string;
    name: string;
    size: number;
    type: string;
}
export interface CatsBotConfig {
    /** WebSocket server URL, e.g. "ws://localhost:6061/v0/channels" */
    serverUrl: string;
    /** Bot API key, e.g. "cc_1a_abc123..." */
    apiKey: string;
    /** Stable id of this bot runtime/body. One bot can only bind to one body. */
    bodyId: string;
    /** Optional install/instance id for diagnostics. */
    installationId?: string;
    /** HTTP base URL for REST endpoints (upload). Defaults to deriving from serverUrl. */
    httpBaseUrl?: string;
    /** Delay in ms before reconnecting after disconnect. Default: 3000 */
    reconnectDelay?: number;
    /** Timeout in ms for establishing the TCP/WebSocket connection. Default: 15000 */
    connectTimeout?: number;
    /** Timeout in ms for waiting on the server's hi/ctrl handshake. Default: 10000 */
    handshakeTimeout?: number;
    /** Timeout in ms for server pings before forcing reconnect. Default: 70000 */
    pingTimeout?: number;
}
import type { MessageContext } from './context';
export interface BotEventMap {
    ready: (uid: string, name: string) => void;
    message: (ctx: MessageContext) => void;
    device_rpc: (msg: MsgDeviceRPC) => void;
    presence: (pres: MsgServerPres) => void;
    typing: (info: MsgServerInfo) => void;
    read: (info: MsgServerInfo) => void;
    ctrl: (ctrl: MsgServerCtrl) => void;
    disconnect: (code: number, reason: string) => void;
    error: (err: Error) => void;
    reconnecting: (attempt: number) => void;
}
//# sourceMappingURL=types.d.ts.map