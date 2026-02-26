import Foundation

/// Lightweight local message cache using UserDefaults.
/// Stores recent messages per topic to survive view lifecycle and app restarts.
@MainActor
class MessageStore {
    static let shared = MessageStore()

    private let defaults = UserDefaults.standard
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()
    private let maxPerTopic = 200

    private func key(for topic: String) -> String {
        "cc_msgs_\(topic)"
    }

    // MARK: - Read / Write

    func loadMessages(for topic: String) -> [Message] {
        guard let data = defaults.data(forKey: key(for: topic)) else { return [] }
        return (try? decoder.decode([Message].self, from: data)) ?? []
    }

    func saveMessages(_ messages: [Message], for topic: String) {
        let trimmed = Array(messages.suffix(maxPerTopic))
        if let data = try? encoder.encode(trimmed) {
            defaults.set(data, forKey: key(for: topic))
        }
    }

    func appendMessage(_ message: Message, for topic: String) {
        var msgs = loadMessages(for: topic)
        if !msgs.contains(where: { $0.seq == message.seq }) {
            msgs.append(message)
            saveMessages(msgs, for: topic)
        }
    }

    func updateMessageSeq(in topic: String, oldSeq: Int, newSeq: Int) {
        var msgs = loadMessages(for: topic)
        if let idx = msgs.firstIndex(where: { $0.seq == oldSeq }) {
            msgs[idx].seq = newSeq
            saveMessages(msgs, for: topic)
        }
    }

    // MARK: - Clear

    func clearMessages(for topic: String) {
        defaults.removeObject(forKey: key(for: topic))
    }

    func clearAllMessages() {
        let allKeys = defaults.dictionaryRepresentation().keys
        for k in allKeys where k.hasPrefix("cc_msgs_") {
            defaults.removeObject(forKey: k)
        }
    }
}
