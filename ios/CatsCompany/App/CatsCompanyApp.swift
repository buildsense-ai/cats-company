import SwiftUI

@main
struct CatsCompanyApp: App {
    @StateObject private var auth = AuthManager.shared

    init() {
        print("🐱 [APP] init start")
        configureTabBarAppearance()
        print("🐱 [APP] init done")
    }

    var body: some Scene {
        WindowGroup {
            SwiftUI.Group {
                let _ = print("🐱 [APP] body eval — isLoggedIn=\(auth.isLoggedIn)")
                if auth.isLoggedIn {
                    MainTabView()
                        .onAppear {
                            print("🐱 [APP] MainTabView appeared")
                            WebSocketManager.shared.connect()
                        }
                } else {
                    LoginView()
                        .onAppear {
                            print("🐱 [APP] LoginView appeared")
                        }
                }
            }
            .tint(CatColor.primary)
        }
    }

    private func configureTabBarAppearance() {
        let appearance = UITabBarAppearance()
        appearance.configureWithDefaultBackground()
        UITabBar.appearance().standardAppearance = appearance
        UITabBar.appearance().scrollEdgeAppearance = appearance
    }
}

struct MainTabView: View {
    var body: some View {
        TabView {
            ChatListView()
                .tabItem {
                    Label("消息", systemImage: "bubble.left.and.bubble.right")
                }

            ContactsView()
                .tabItem {
                    Label("通讯录", systemImage: "person.2")
                }

            ProfileView()
                .tabItem {
                    Label("我", systemImage: "person.circle")
                }
        }
    }
}
