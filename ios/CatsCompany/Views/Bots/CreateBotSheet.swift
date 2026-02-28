import SwiftUI

struct CreateBotSheet: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject private var auth = AuthManager.shared
    @State private var displayName = ""
    @State private var isCreating = false
    @State private var errorMessage: String?
    @State private var createdBot: APIClient.CreateBotResponse?
    @State private var createdApiKey: String?
    @State private var friendStatus: String?
    @State private var friendSuccess = false
    @State private var copiedField: String?
    let onCreated: () async -> Void

    private var wsUrl: String {
        let base = APIClient.shared.baseURL
            .replacingOccurrences(of: "http://", with: "ws://")
            .replacingOccurrences(of: "https://", with: "wss://")
        return "\(base)/v0/channels"
    }

    var body: some View {
        NavigationStack {
            Form {
                if let bot = createdBot {
                    successSection(bot)
                } else {
                    createSection
                }
            }
            .navigationTitle(createdBot != nil ? "创建成功" : "创建机器人")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if createdBot != nil {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("完成") {
                            Task {
                                await onCreated()
                                dismiss()
                            }
                        }
                    }
                } else {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("取消") { dismiss() }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button("创建") {
                            Task { await createBot() }
                        }
                        .disabled(displayName.trimmingCharacters(in: .whitespaces).isEmpty || isCreating)
                    }
                }
            }
        }
    }

    // MARK: - Create Form

    private var createSection: some View {
        Group {
            Section {
                TextField("机器人名称", text: $displayName)
                    .textInputAutocapitalization(.never)
            } header: {
                Text("基本信息")
            } footer: {
                Text("名称将作为机器人的显示名，用户名会自动生成")
            }

            if let err = errorMessage {
                Section {
                    Text(err)
                        .foregroundStyle(CatColor.danger)
                        .font(.caption)
                }
            }
        }
    }

    // MARK: - Success View

    private func successSection(_ bot: APIClient.CreateBotResponse) -> some View {
        Group {
            Section {
                LabeledContent("名称", value: bot.displayName ?? bot.username)
                LabeledContent("用户名", value: "@\(bot.username)")
                LabeledContent("UID", value: "\(bot.uid)")
            } header: {
                Text("机器人信息")
            }

            if let key = createdApiKey {
                Section {
                    copiableRow(label: "API Key", value: key, field: "apiKey")
                    copiableRow(label: "WebSocket", value: wsUrl, field: "wsUrl")
                } header: {
                    Text("连接凭证")
                } footer: {
                    Text("API Key 仅在创建时显示一次，请妥善保存。")
                        .foregroundStyle(CatColor.danger)
                }
            }

            if let status = friendStatus {
                Section {
                    HStack {
                        Image(systemName: friendSuccess ? "checkmark.circle.fill" : "exclamationmark.circle.fill")
                            .foregroundStyle(friendSuccess ? .green : .orange)
                        Text(status)
                            .font(.caption)
                    }
                } header: {
                    Text("好友状态")
                }
            }
        }
    }

    private func copiableRow(label: String, value: String, field: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            HStack {
                Text(value)
                    .font(.system(.caption, design: .monospaced))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
                Button {
                    UIPasteboard.general.string = value
                    copiedField = field
                    Task {
                        try? await Task.sleep(nanoseconds: 2_000_000_000)
                        if copiedField == field { copiedField = nil }
                    }
                } label: {
                    Image(systemName: copiedField == field ? "checkmark" : "doc.on.doc")
                        .font(.caption)
                        .foregroundStyle(copiedField == field ? .green : CatColor.primary)
                }
                .buttonStyle(.borderless)
            }
        }
    }

    // MARK: - Actions

    private func createBot() async {
        let name = displayName.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else { return }

        isCreating = true
        errorMessage = nil

        let slug = name.lowercased()
            .replacingOccurrences(of: " ", with: "-")
            .filter { $0.isLetter || $0.isNumber || $0 == "-" }
        let username = "bot-\(slug.prefix(16))-\(Int.random(in: 1000...9999))"

        do {
            let resp = try await APIClient.shared.createBot(username: username, displayName: name)
            createdBot = resp
            createdApiKey = resp.apiKey

            // Auto-add bot as friend
            await autoAddFriend(botUid: resp.uid, apiKey: resp.apiKey)
        } catch {
            errorMessage = error.localizedDescription
            isCreating = false
        }
    }

    private func autoAddFriend(botUid: Int64, apiKey: String?) async {
        guard let myUid = auth.currentUser?.id else {
            friendStatus = "无法获取当前用户，请手动添加好友"
            return
        }
        do {
            // Step 1: Owner sends friend request to bot
            _ = try await APIClient.shared.sendFriendRequest(userId: botUid)

            // Step 2: Bot accepts using its ApiKey
            if let key = apiKey {
                try await APIClient.shared.acceptFriendAsBot(apiKey: key, userId: myUid)
                friendSuccess = true
                friendStatus = "已自动添加为好友"
            } else {
                friendStatus = "已发送好友请求，需手动接受"
            }
        } catch {
            friendStatus = "自动添加好友失败: \(error.localizedDescription)"
        }
    }
}
