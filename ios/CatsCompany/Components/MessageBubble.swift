import SwiftUI
import QuickLook

struct MessageBubble: View {
    let message: Message
    let isMe: Bool
    var onReply: (() -> Void)?

    @State private var showImagePreview = false
    @State private var previewImageUrl: String?

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if isMe { Spacer(minLength: 60) }

            if !isMe {
                AvatarView(name: message.fromUid, size: 32)
            }

            VStack(alignment: isMe ? .trailing : .leading, spacing: 4) {
                if !isMe {
                    Text(message.fromUid)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }

                // Reply quote
                if message.replyTo != nil {
                    HStack(spacing: 4) {
                        RoundedRectangle(cornerRadius: 1.5)
                            .fill(CatColor.primary.opacity(0.6))
                            .frame(width: 2.5)
                        Text("回复消息 #\(message.replyTo!)")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                }

                // Content
                contentView
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(isMe ? CatColor.bubbleSelf : CatColor.bubbleOther)
                    .foregroundStyle(isMe ? CatColor.bubbleSelfText : CatColor.textPrimary)
                    .clipShape(RoundedRectangle(cornerRadius: CatLayout.radius))
                    .contextMenu {
                        Button { onReply?() } label: {
                            Label("回复", systemImage: "arrowshape.turn.up.left")
                        }
                        Button {
                            UIPasteboard.general.string = message.content.displayText
                        } label: {
                            Label("复制", systemImage: "doc.on.doc")
                        }
                    }
            }

            if !isMe { Spacer(minLength: 60) }
        }
        .fullScreenCover(isPresented: $showImagePreview) {
            ImagePreviewView(urlString: previewImageUrl) {
                showImagePreview = false
            }
        }
    }

    @ViewBuilder
    private var contentView: some View {
        switch message.content {
        case .text(let text):
            Text(text)
                .font(.body)

        case .rich(let rich):
            VStack(alignment: .leading, spacing: 6) {
                if rich.type == "image", let urlStr = rich.url ?? rich.imageUrl {
                    let fullUrl = urlStr.hasPrefix("http") ? urlStr : APIClient.shared.baseURL + urlStr
                    AsyncImage(url: URL(string: fullUrl)) { image in
                        image
                            .resizable()
                            .scaledToFit()
                            .frame(maxWidth: 200, maxHeight: 200)
                            .clipShape(RoundedRectangle(cornerRadius: 8))
                            .onTapGesture {
                                previewImageUrl = fullUrl
                                showImagePreview = true
                            }
                    } placeholder: {
                        ProgressView()
                            .frame(width: 100, height: 100)
                    }
                } else if rich.type == "file" {
                    FileContentView(rich: rich)
                } else if rich.type == "link" || rich.type == "card" {
                    VStack(alignment: .leading, spacing: 4) {
                        if let title = rich.title {
                            Text(title).font(.subheadline.bold())
                        }
                        if let desc = rich.description {
                            Text(desc).font(.caption).lineLimit(2)
                        }
                        if let imgUrl = rich.imageUrl, let url = URL(string: imgUrl) {
                            AsyncImage(url: url) { image in
                                image.resizable().scaledToFit()
                                    .frame(maxHeight: 120)
                                    .clipShape(RoundedRectangle(cornerRadius: 6))
                            } placeholder: { EmptyView() }
                        }
                    }
                } else {
                    Text(rich.text ?? rich.title ?? "")
                        .font(.body)
                }
            }
        }
    }

    private func formatFileSize(_ bytes: Int) -> String {
        if bytes < 1024 { return "\(bytes) B" }
        if bytes < 1024 * 1024 { return "\(bytes / 1024) KB" }
        return String(format: "%.1f MB", Double(bytes) / 1024.0 / 1024.0)
    }
}

// MARK: - File Content View

struct FileContentView: View {
    let rich: RichContent
    @State private var isDownloading = false
    @State private var downloadProgress: Double = 0
    @State private var previewURL: URL?
    @State private var showPreview = false

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: fileIcon)
                .font(.title)
                .foregroundStyle(iconColor)

            VStack(alignment: .leading, spacing: 2) {
                Text(rich.fileName ?? "文件")
                    .font(.subheadline.bold())
                    .lineLimit(1)
                HStack(spacing: 4) {
                    if let size = rich.fileSize {
                        Text(formatFileSize(size))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    if isPreviewable {
                        Text("· 可预览")
                            .font(.caption)
                            .foregroundStyle(CatColor.primary)
                    }
                }
            }

            Spacer()

            if isDownloading {
                ProgressView(value: downloadProgress) {
                    Text("\(Int(downloadProgress * 100))%")
                        .font(.caption2)
                }
                .frame(width: 50)
            } else {
                Button {
                    if isPreviewable {
                        downloadAndPreview()
                    } else {
                        downloadFile()
                    }
                } label: {
                    Image(systemName: isPreviewable ? "eye.circle.fill" : "arrow.down.circle.fill")
                        .font(.title2)
                        .foregroundStyle(CatColor.primary)
                }
            }
        }
        .frame(minWidth: 180, maxWidth: 240)
        .onTapGesture {
            if isPreviewable && !isDownloading {
                downloadAndPreview()
            }
        }
        .quickLookPreview($previewURL)
    }

    private var isPreviewable: Bool {
        guard let name = rich.fileName?.lowercased() else { return false }
        let exts = [".pdf", ".docx", ".doc", ".xlsx", ".xls",
                    ".pptx", ".ppt", ".csv", ".rtf", ".txt",
                    ".png", ".jpg", ".jpeg", ".gif", ".heic"]
        return exts.contains(where: { name.hasSuffix($0) })
    }

    private var fileIcon: String {
        guard let name = rich.fileName?.lowercased() else { return "doc.fill" }
        if name.hasSuffix(".pdf") { return "doc.text.fill" }
        if name.hasSuffix(".docx") || name.hasSuffix(".doc") { return "doc.richtext.fill" }
        if name.hasSuffix(".xlsx") || name.hasSuffix(".xls") || name.hasSuffix(".csv") { return "tablecells.fill" }
        if name.hasSuffix(".pptx") || name.hasSuffix(".ppt") { return "rectangle.fill.on.rectangle.fill" }
        if name.hasSuffix(".zip") || name.hasSuffix(".rar") { return "doc.zipper" }
        if name.hasSuffix(".mp3") || name.hasSuffix(".wav") { return "music.note" }
        if name.hasSuffix(".mp4") || name.hasSuffix(".mov") { return "video.fill" }
        if name.hasSuffix(".txt") || name.hasSuffix(".rtf") { return "doc.plaintext.fill" }
        return "doc.fill"
    }

    private var iconColor: Color {
        guard let name = rich.fileName?.lowercased() else { return CatColor.primary }
        if name.hasSuffix(".pdf") { return .red }
        if name.hasSuffix(".docx") || name.hasSuffix(".doc") { return .blue }
        if name.hasSuffix(".xlsx") || name.hasSuffix(".xls") || name.hasSuffix(".csv") { return .green }
        if name.hasSuffix(".pptx") || name.hasSuffix(".ppt") { return .orange }
        return CatColor.primary
    }

    private func formatFileSize(_ bytes: Int) -> String {
        if bytes < 1024 { return "\(bytes) B" }
        if bytes < 1024 * 1024 { return "\(bytes / 1024) KB" }
        return String(format: "%.1f MB", Double(bytes) / 1024.0 / 1024.0)
    }

    private func resolveFullURL() -> URL? {
        guard let urlStr = rich.url else { return nil }
        let full = urlStr.hasPrefix("http") ? urlStr : APIClient.shared.baseURL + urlStr
        return URL(string: full)
    }

    private func downloadAndPreview() {
        guard let url = resolveFullURL() else { return }
        isDownloading = true

        Task {
            do {
                let (tempURL, _) = try await URLSession.shared.download(from: url)
                let fileName = rich.fileName ?? url.lastPathComponent
                let dest = FileManager.default.temporaryDirectory.appendingPathComponent(fileName)
                try? FileManager.default.removeItem(at: dest)
                try FileManager.default.moveItem(at: tempURL, to: dest)
                isDownloading = false
                previewURL = dest
            } catch {
                isDownloading = false
                print("Preview download error: \(error)")
            }
        }
    }

    private func downloadFile() {
        guard let url = resolveFullURL() else { return }
        isDownloading = true
        downloadProgress = 0

        Task {
            do {
                let (localURL, _) = try await URLSession.shared.download(from: url)
                isDownloading = false
                let activityVC = UIActivityViewController(activityItems: [localURL], applicationActivities: nil)
                if let windowScene = UIApplication.shared.connectedScenes.first as? UIWindowScene,
                   let rootVC = windowScene.windows.first?.rootViewController {
                    rootVC.present(activityVC, animated: true)
                }
            } catch {
                isDownloading = false
                print("Download error: \(error)")
            }
        }
    }
}

// MARK: - Image Preview View

struct ImagePreviewView: View {
    let urlString: String?
    let onDismiss: () -> Void

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()

            if let urlStr = urlString, let url = URL(string: urlStr) {
                AsyncImage(url: url) { image in
                    image
                        .resizable()
                        .scaledToFit()
                        .gesture(
                            TapGesture(count: 1)
                                .onEnded { onDismiss() }
                        )
                } placeholder: {
                    ProgressView()
                        .tint(.white)
                }
            }

            VStack {
                HStack {
                    Spacer()
                    Button {
                        onDismiss()
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.title)
                            .foregroundStyle(.white)
                            .padding()
                    }
                }
                Spacer()
            }
        }
    }
}
