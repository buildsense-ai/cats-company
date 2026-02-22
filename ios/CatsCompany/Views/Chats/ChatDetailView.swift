import SwiftUI

struct ChatDetailView: View {
    let topicId: String
    let title: String

    @ObservedObject var ws = WebSocketManager.shared
    @ObservedObject var auth = AuthManager.shared
    @State private var messages: [Message] = []
    @State private var inputText = ""
    @State private var replyTo: Message?
    @State private var isTyping = false
    @State private var typingUser: String?
    @State private var isLoading = true

    var body: some View {
        VStack(spacing: 0) {
            // Messages
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 8) {
                        ForEach(Array(messages.enumerated()), id: \.element.seq) { index, msg in
                            // Date separator
                            if let ts = msg.timestamp, shouldShowDate(at: index) {
                                DateSeparator(date: ts)
                            }

                            MessageBubble(
                                message: msg,
                                isMe: msg.fromUid == String(auth.currentUser?.id ?? 0),
                                onReply: { replyTo = msg }
                            )
                            .id(msg.seq)
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                }
                .background(CatColor.chatBg)
                .scrollDismissesKeyboard(.interactively)
                .onTapGesture {
                    hideKeyboard()
                }
                .onChange(of: messages.count) {
                    if let last = messages.last {
                        withAnimation {
                            proxy.scrollTo(last.seq, anchor: .bottom)
                        }
                    }
                }
            }

            // Typing indicator
            if let who = typingUser {
                HStack {
                    Text("\(who) 正在输入...")
                        .font(.caption)
                        .foregroundStyle(CatColor.textSecondary)
                    Spacer()
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 4)
                .background(CatColor.chatBg)
            }

            // Reply preview
            if let reply = replyTo {
                HStack {
                    RoundedRectangle(cornerRadius: 2)
                        .fill(CatColor.primary)
                        .frame(width: 3)
                    VStack(alignment: .leading) {
                        Text("回复")
                            .font(.caption.bold())
                            .foregroundStyle(CatColor.primary)
                        Text(reply.content.displayText)
                            .font(.caption)
                            .lineLimit(1)
                    }
                    Spacer()
                    Button { replyTo = nil } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(CatColor.secondaryBg)
            }

            // Input bar
            VStack(spacing: 0) {
                CatColor.border
                    .frame(height: 0.5)

                HStack(spacing: 8) {
                    TextField("输入消息...", text: $inputText, axis: .vertical)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .lineLimit(1...5)
                        .background(CatColor.background)
                        .clipShape(RoundedRectangle(cornerRadius: CatLayout.avatarRadius))
                        .onChange(of: inputText) {
                            ws.sendTyping(topic: topicId)
                        }

                    Button {
                        sendMessage()
                    } label: {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.title2)
                            .foregroundStyle(
                                inputText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                                    ? Color.gray.opacity(0.4)
                                    : CatColor.primary
                            )
                    }
                    .disabled(inputText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
            }
            .background(CatColor.cardBg)
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .task { await loadMessages() }
        .onAppear { setupWSHandlers() }
        .onDisappear { clearWSHandlers() }
    }

    private func shouldShowDate(at index: Int) -> Bool {
        guard let currentTs = messages[index].timestamp else { return false }
        if index == 0 { return true }
        guard let prevTs = messages[index - 1].timestamp else { return true }
        return currentTs.timeIntervalSince(prevTs) > 300 // 5 minutes
    }

    private func setupWSHandlers() {
        ws.onData = { data in
            guard data.topic == topicId else { return }
            let msg = Message(
                id: nil,
                topicId: data.topic,
                fromUid: data.from ?? "",
                content: data.content,
                seq: data.seq,
                replyTo: data.replyTo
            )
            if !messages.contains(where: { $0.seq == msg.seq }) {
                messages.append(msg)
            }
            ws.sendRead(topic: topicId, seq: data.seq)
            typingUser = nil
        }
        ws.onInfo = { info in
            guard info.topic == topicId else { return }
            if info.what == "kp" {
                typingUser = info.from
                Task {
                    try? await Task.sleep(nanoseconds: 3_000_000_000)
                    if typingUser == info.from {
                        typingUser = nil
                    }
                }
            }
        }
    }

    private func clearWSHandlers() {
        ws.onData = nil
        ws.onInfo = nil
    }

    private func loadMessages() async {
        do {
            let msgs = try await APIClient.shared.getMessages(topicId: topicId, limit: 50)
            messages = msgs
            if let last = msgs.last {
                ws.updateTopicSeq(topicId, seq: last.seq)
                ws.sendRead(topic: topicId, seq: last.seq)
            }
            isLoading = false
        } catch {
            print("Load messages error: \(error)")
            isLoading = false
        }
    }

    private func sendMessage() {
        let text = inputText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }

        let myUid = String(auth.currentUser?.id ?? 0)
        let seq = (messages.last?.seq ?? 0) + 1

        let msg = Message(
            id: nil,
            topicId: topicId,
            fromUid: myUid,
            content: .text(text),
            seq: seq,
            replyTo: replyTo?.seq
        )
        messages.append(msg)

        ws.sendMessage(topic: topicId, content: text, replyTo: replyTo?.seq)
        inputText = ""
        replyTo = nil
    }

    private func hideKeyboard() {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }
}
