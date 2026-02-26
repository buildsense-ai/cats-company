import Foundation
import Combine

/// Manages WebSocket connection, message routing, and auto-reconnect.
@MainActor
class WebSocketManager: ObservableObject {
    static let shared = WebSocketManager()

    @Published var isConnected = false

    private var webSocket: URLSessionWebSocketTask?
    private var session: URLSession = .shared
    private var reconnectTask: Task<Void, Never>?
    private var msgIdCounter = 0
    private var topicLastSeq: [String: Int] = [:]

    // Callbacks
    var onData: ((WSData) -> Void)?
    var onPres: ((WSPres) -> Void)?
    var onInfo: ((WSInfo) -> Void)?
    var onCtrl: ((WSCtrl) -> Void)?
    var onFriend: ((WSFriendEvent) -> Void)?
    var onConnected: (() -> Void)?
    var onDisconnected: (() -> Void)?

    private init() {}

    func nextId() -> String {
        msgIdCounter += 1
        return String(msgIdCounter)
    }

    // MARK: - Connect / Disconnect

    func connect() {
        guard let token = AuthManager.shared.token else { return }
        disconnect(reconnect: false)

        let base = APIClient.shared.baseURL
            .replacingOccurrences(of: "http://", with: "ws://")
            .replacingOccurrences(of: "https://", with: "wss://")
        let urlStr = "\(base)/v0/channels?token=\(token)"
        guard let url = URL(string: urlStr) else { return }

        webSocket = session.webSocketTask(with: url)
        webSocket?.resume()
        isConnected = true
        receiveLoop()

        // Handshake
        send(ClientMessage(hi: WSHi(id: nextId())))
        // Request online status
        send(ClientMessage(get: WSGet(id: nextId(), topic: "me", what: "online")))
        // Request missed messages
        for (topic, _) in topicLastSeq {
            requestMissedMessages(topic: topic)
        }

        onConnected?()
    }

    func disconnect(reconnect: Bool = false) {
        reconnectTask?.cancel()
        reconnectTask = nil
        webSocket?.cancel(with: .goingAway, reason: nil)
        webSocket = nil
        if isConnected {
            isConnected = false
            onDisconnected?()
        }
        if reconnect {
            scheduleReconnect()
        }
    }

    private func scheduleReconnect() {
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 3_000_000_000) // 3s
            guard !Task.isCancelled else { return }
            await self?.connect()
        }
    }

    // MARK: - Send

    func send(_ msg: ClientMessage) {
        guard let ws = webSocket else { return }
        let encoder = JSONEncoder()
        guard let data = try? encoder.encode(msg) else { return }
        ws.send(.string(String(data: data, encoding: .utf8)!)) { error in
            if let error {
                print("[WS] Send error: \(error)")
            }
        }
    }

    @discardableResult
    func sendMessage(topic: String, content: String, replyTo: Int? = nil) -> String {
        let msgId = nextId()
        let pub = WSPub(id: msgId, topic: topic, content: content, replyTo: replyTo)
        send(ClientMessage(pub: pub))
        return msgId
    }

    func sendTyping(topic: String) {
        send(ClientMessage(note: WSNote(topic: topic, what: "kp")))
    }

    func sendRead(topic: String, seq: Int) {
        send(ClientMessage(note: WSNote(topic: topic, what: "read", seq: seq)))
    }

    func updateTopicSeq(_ topic: String, seq: Int) {
        if seq > (topicLastSeq[topic] ?? 0) {
            topicLastSeq[topic] = seq
        }
    }

    func requestMissedMessages(topic: String) {
        guard let lastSeq = topicLastSeq[topic], lastSeq > 0 else { return }
        send(ClientMessage(get: WSGet(id: nextId(), topic: topic, what: "history", seq: lastSeq)))
    }

    // MARK: - Receive

    private func receiveLoop() {
        webSocket?.receive { [weak self] result in
            Task { @MainActor in
                switch result {
                case .success(let message):
                    switch message {
                    case .string(let text):
                        self?.handleMessage(text)
                    case .data(let data):
                        if let text = String(data: data, encoding: .utf8) {
                            self?.handleMessage(text)
                        }
                    @unknown default:
                        break
                    }
                    self?.receiveLoop()
                case .failure(let error):
                    print("[WS] Receive error: \(error)")
                    self?.disconnect(reconnect: true)
                }
            }
        }
    }

    private func handleMessage(_ text: String) {
        guard let data = text.data(using: .utf8) else { return }
        guard let msg = try? JSONDecoder().decode(ServerMessage.self, from: data) else {
            print("[WS] Failed to decode: \(text.prefix(200))")
            return
        }

        if let ctrl = msg.ctrl {
            onCtrl?(ctrl)
        }
        if let d = msg.data {
            updateTopicSeq(d.topic, seq: d.seq)
            onData?(d)
        }
        if let pres = msg.pres {
            onPres?(pres)
        }
        if let info = msg.info {
            onInfo?(info)
        }
        if let friend = msg.friend {
            onFriend?(friend)
        }
    }
}
