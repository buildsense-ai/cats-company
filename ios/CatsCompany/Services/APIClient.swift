import Foundation

/// REST API client matching the backend endpoints.
@MainActor
class APIClient {
    static let shared = APIClient()

    /// Simulator → localhost; real device → Mac's LAN IP.
    #if targetEnvironment(simulator)
    var baseURL = "http://localhost:6061"
    #else
    var baseURL = "http://192.168.31.15:6061"
    #endif

    private var token: String? { AuthManager.shared.token }

    // MARK: - Auth

    struct AuthResponse: Decodable {
        let token: String
        let uid: Int64
        let username: String
        let displayName: String?

        enum CodingKeys: String, CodingKey {
            case token, uid, username
            case displayName = "display_name"
        }

        var user: User {
            User(id: uid, username: username, displayName: displayName)
        }
    }

    func register(username: String, password: String, displayName: String?) async throws -> AuthResponse {
        var body: [String: Any] = ["username": username, "password": password]
        if let dn = displayName, !dn.isEmpty { body["display_name"] = dn }
        return try await request(.post, "/api/auth/register", body: body)
    }

    func login(username: String, password: String) async throws -> AuthResponse {
        try await request(.post, "/api/auth/login", body: [
            "username": username,
            "password": password
        ])
    }

    // MARK: - Response Wrappers

    private struct FriendsResponse: Decodable { let friends: [User]? }
    private struct RequestsResponse: Decodable { let requests: [FriendRequest]? }
    private struct UsersResponse: Decodable { let users: [User]? }
    private struct MessagesResponse: Decodable { let messages: [Message]? }
    private struct GroupsResponse: Decodable { let groups: [Group]? }

    // MARK: - Friends

    func getFriends() async throws -> [User] {
        let resp: FriendsResponse = try await request(.get, "/api/friends")
        return resp.friends ?? []
    }

    func getPendingRequests() async throws -> [FriendRequest] {
        let resp: RequestsResponse = try await request(.get, "/api/friends/pending")
        return resp.requests ?? []
    }

    func sendFriendRequest(userId: Int64, message: String? = nil) async throws -> EmptyResponse {
        var body: [String: Any] = ["user_id": userId]
        if let m = message { body["message"] = m }
        return try await request(.post, "/api/friends/request", body: body)
    }

    func acceptFriend(userId: Int64) async throws -> EmptyResponse {
        try await request(.post, "/api/friends/accept", body: ["user_id": userId])
    }

    func rejectFriend(userId: Int64) async throws -> EmptyResponse {
        try await request(.post, "/api/friends/reject", body: ["user_id": userId])
    }

    func blockUser(userId: Int64) async throws -> EmptyResponse {
        try await request(.post, "/api/friends/block", body: ["user_id": userId])
    }

    func removeFriend(userId: Int64) async throws -> EmptyResponse {
        try await request(.delete, "/api/friends/remove?user_id=\(userId)")
    }

    func searchUsers(query: String) async throws -> [User] {
        let q = query.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? query
        let resp: UsersResponse = try await request(.get, "/api/users/search?q=\(q)")
        return resp.users ?? []
    }

    func getOnlineStatus() async throws -> [String: Bool] {
        try await request(.get, "/api/users/online")
    }

    // MARK: - Messages

    func getMessages(topicId: String, limit: Int = 50, offset: Int = 0) async throws -> [Message] {
        let t = topicId.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? topicId
        let resp: MessagesResponse = try await request(.get, "/api/messages?topic_id=\(t)&limit=\(limit)&offset=\(offset)")
        return resp.messages ?? []
    }

    func sendMessage(topicId: String, content: String) async throws -> EmptyResponse {
        try await request(.post, "/api/messages/send", body: [
            "topic_id": topicId,
            "content": content,
            "msg_type": "text"
        ])
    }

    // MARK: - Groups

    func getGroups() async throws -> [Group] {
        let resp: GroupsResponse = try await request(.get, "/api/groups")
        return resp.groups ?? []
    }

    func createGroup(name: String, memberIds: [Int64]) async throws -> Group {
        try await request(.post, "/api/groups/create", body: [
            "name": name,
            "member_ids": memberIds
        ] as [String: Any])
    }

    func getGroupInfo(groupId: Int64) async throws -> Group {
        try await request(.get, "/api/groups/info?id=\(groupId)")
    }

    func inviteToGroup(groupId: Int64, userIds: [Int64]) async throws -> EmptyResponse {
        try await request(.post, "/api/groups/invite", body: [
            "group_id": groupId,
            "user_ids": userIds
        ] as [String: Any])
    }

    func leaveGroup(groupId: Int64) async throws -> EmptyResponse {
        try await request(.post, "/api/groups/leave", body: ["group_id": groupId])
    }

    func kickMember(groupId: Int64, userId: Int64) async throws -> EmptyResponse {
        try await request(.post, "/api/groups/kick", body: [
            "group_id": groupId,
            "user_id": userId
        ])
    }

    func disbandGroup(groupId: Int64) async throws -> EmptyResponse {
        try await request(.post, "/api/groups/disband", body: ["group_id": groupId])
    }

    // MARK: - Upload

    func uploadImage(data: Data, filename: String) async throws -> UploadResponse {
        try await upload("/api/upload?type=image", data: data, filename: filename, mimeType: "image/jpeg")
    }

    struct UploadResponse: Decodable {
        let url: String
    }

    // MARK: - Internal

    struct EmptyResponse: Decodable {}

    enum HTTPMethod: String {
        case get = "GET", post = "POST", delete = "DELETE"
    }

    private func request<T: Decodable>(_ method: HTTPMethod, _ path: String, body: [String: Any]? = nil) async throws -> T {
        guard let url = URL(string: baseURL + path) else {
            throw APIError.invalidURL
        }

        var req = URLRequest(url: url)
        req.httpMethod = method.rawValue
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let t = token {
            req.setValue("Bearer \(t)", forHTTPHeaderField: "Authorization")
        }
        if let body = body {
            req.httpBody = try JSONSerialization.data(withJSONObject: body)
        }

        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.unknown
        }
        if http.statusCode >= 400 {
            if let err = try? JSONDecoder().decode(ErrorResponse.self, from: data) {
                throw APIError.server(err.error)
            }
            throw APIError.httpError(http.statusCode)
        }
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func upload(_ path: String, data: Data, filename: String, mimeType: String) async throws -> UploadResponse {
        guard let url = URL(string: baseURL + path) else { throw APIError.invalidURL }

        let boundary = UUID().uuidString
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        if let t = token {
            req.setValue("Bearer \(t)", forHTTPHeaderField: "Authorization")
        }

        var body = Data()
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: \(mimeType)\r\n\r\n".data(using: .utf8)!)
        body.append(data)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)
        req.httpBody = body

        let (respData, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse, http.statusCode < 400 else {
            throw APIError.unknown
        }
        return try JSONDecoder().decode(UploadResponse.self, from: respData)
    }

    private struct ErrorResponse: Decodable {
        let error: String
    }
}

enum APIError: LocalizedError {
    case invalidURL
    case httpError(Int)
    case server(String)
    case unknown

    var errorDescription: String? {
        switch self {
        case .invalidURL: return "无效的URL"
        case .httpError(let code): return "请求失败 (\(code))"
        case .server(let msg): return msg
        case .unknown: return "未知错误"
        }
    }
}
