import type { CatsBot } from './bot';
import type { CatsCoIdentityMetadata, DeviceSelection, MsgServerData, MessageContent, ScopedDeviceGrant } from './types';
import { type TopicInfo } from './topic';
export interface TypingHeartbeatOptions {
    /** Interval between typing pings. Default: 2500ms */
    intervalMs?: number;
}
export declare class MessageContext {
    readonly bot: CatsBot;
    readonly topic: string;
    readonly from: string;
    readonly seq: number;
    readonly content: unknown;
    readonly metadata: Record<string, unknown> | undefined;
    readonly replyTo: number | undefined;
    constructor(bot: CatsBot, data: MsgServerData);
    /** Extract plain text from content (returns stringified JSON for rich content). */
    get text(): string;
    /** Whether this is a P2P (direct message) topic. */
    get isP2P(): boolean;
    /** Whether this is a group topic. */
    get isGroup(): boolean;
    /** Parsed topic info with peer/group identification. */
    get topicInfo(): TopicInfo;
    /** Server-canonical CatsCo identity metadata attached to this turn. */
    get catscoIdentity(): CatsCoIdentityMetadata | undefined;
    /** Device grants the bot can use for this exact turn, if any. */
    get deviceGrants(): ScopedDeviceGrant[];
    /** Server-selected device context for this turn, if available. */
    get deviceSelection(): DeviceSelection | undefined;
    /** Reply with content to the same topic. */
    reply(content: MessageContent): Promise<number>;
    /** Send typing indicator, wait, then reply. */
    replyWithTyping(content: MessageContent, delay?: number): Promise<number>;
    /**
     * Keep typing active while an async task runs.
     * Sends an immediate typing ping, then heartbeats until the task settles.
     */
    withTyping<T>(task: () => Promise<T>, options?: TypingHeartbeatOptions): Promise<T>;
    /** Send a typing indicator to this topic. */
    sendTyping(): Promise<void>;
    /** Mark messages up to this seq as read. */
    markRead(): Promise<void>;
}
//# sourceMappingURL=context.d.ts.map