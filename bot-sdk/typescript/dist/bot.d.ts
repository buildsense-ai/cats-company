import type { CatsBotConfig, BotEventMap, MsgServerData, MsgDeviceRPC, DeviceRPCAckParams, DeviceRPCRequestAck, DeviceRPCRequestInput, DeviceRPCResultInput, MessageContent, RichContentLinkPreview, RichContentCard, UploadResult } from './types';
export declare class CatsBot {
    uid: string;
    name: string;
    private readonly config;
    private readonly emitter;
    private readonly uploader;
    private readonly pendingAcks;
    private readonly topicLastSeq;
    private ws;
    private msgId;
    private reconnectAttempt;
    private closed;
    private pingTimer;
    private hasConversationBaseline;
    private recoveryPromise;
    private closeSocket;
    constructor(config: CatsBotConfig);
    on<K extends keyof BotEventMap>(event: K, listener: BotEventMap[K]): this;
    off<K extends keyof BotEventMap>(event: K, listener: BotEventMap[K]): this;
    once<K extends keyof BotEventMap>(event: K, listener: BotEventMap[K]): this;
    private emit;
    /**
     * Open the WebSocket connection and perform the handshake.
     * Resolves when the handshake ctrl 200 is received.
     */
    connect(): Promise<void>;
    /**
     * Connect and block until the process is interrupted or disconnect() is called.
     */
    run(): Promise<void>;
    /**
     * Gracefully close the connection. No automatic reconnect.
     */
    disconnect(): void;
    /**
     * Publish a message to a topic. Returns the server-assigned seq number.
     */
    sendMessage(topic: string, content: MessageContent, replyTo?: number): Promise<number>;
    /** Send an image message (from an UploadResult). */
    sendImage(topic: string, upload: UploadResult, opts?: {
        width?: number;
        height?: number;
    }): Promise<number>;
    /** Send a file message (from an UploadResult). */
    sendFile(topic: string, upload: UploadResult, mimeType?: string): Promise<number>;
    /** Send a link preview card. */
    sendLinkPreview(topic: string, payload: RichContentLinkPreview['payload']): Promise<number>;
    /** Send a rich card. */
    sendCard(topic: string, payload: RichContentCard['payload']): Promise<number>;
    /** Send a typing indicator. */
    sendTyping(topic: string): void;
    /** Send a read receipt for messages up to seq. */
    sendReadReceipt(topic: string, seq: number): void;
    /**
     * Send a raw device_rpc envelope. Resolves when the server acknowledges
     * accepting or rejecting the envelope; device results arrive via the
     * `device_rpc` event.
     */
    sendDeviceRPC(msg: Omit<MsgDeviceRPC, 'id'> & {
        id?: string;
    }): Promise<DeviceRPCAckParams>;
    /**
     * Request execution on the currently selected device grant. The returned
     * request_id can be matched with a later `device_rpc` result event.
     */
    sendDeviceRPCRequest(input: DeviceRPCRequestInput): Promise<DeviceRPCRequestAck>;
    /** Send a result for a device_rpc request routed to this connection. */
    sendDeviceRPCResult(input: DeviceRPCResultInput): Promise<DeviceRPCAckParams>;
    /** Fetch message history for a topic since a given seq. */
    getHistory(topic: string, sinceSeq?: number): Promise<MsgServerData[]>;
    /** Upload a file from disk path. */
    uploadFile(filePath: string, type?: 'image' | 'file'): Promise<UploadResult>;
    /** Upload a buffer. */
    uploadBuffer(buffer: Buffer, filename: string, type?: 'image' | 'file'): Promise<UploadResult>;
    private nextId;
    private nextDeviceRPCRequestId;
    private sendRaw;
    private sendWithAck;
    private sendWithCtrlAck;
    private resolveAck;
    private rejectAllPending;
    private doConnect;
    private dispatch;
    private resetPingTimer;
    private clearPingTimer;
    private noteTopicSeq;
    private recoverMissedMessages;
    private fetchConversationCursors;
    private scheduleReconnect;
}
//# sourceMappingURL=bot.d.ts.map