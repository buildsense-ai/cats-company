package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/openchat/openchat/server"
	"github.com/openchat/openchat/server/db/mysql"
	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

func envString(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func isProductionEnv() bool {
	for _, name := range []string{"OC_ENV", "APP_ENV", "GO_ENV", "ENV"} {
		switch strings.ToLower(envString(name)) {
		case "prod", "production":
			return true
		}
	}
	return false
}

func configureJWTSecret() error {
	secret := envString("OC_JWT_SECRET")
	if secret != "" {
		server.SetJWTSecret(secret)
		return nil
	}

	if isProductionEnv() {
		return fmt.Errorf("OC_JWT_SECRET is required when running in production")
	}

	log.Printf("OC_JWT_SECRET not set; using an ephemeral in-memory secret (development only)")
	return nil
}

func buildMySQLPoolConfig(cfg DBConfig) mysql.PoolConfig {
	pool := mysql.DefaultPoolConfig()
	if cfg.MaxOpenConns > 0 {
		pool.MaxOpenConns = cfg.MaxOpenConns
	}
	if cfg.MaxIdleConns > 0 {
		pool.MaxIdleConns = cfg.MaxIdleConns
	}
	if pool.MaxOpenConns > 0 && pool.MaxIdleConns > pool.MaxOpenConns {
		pool.MaxIdleConns = pool.MaxOpenConns
	}
	if cfg.ConnMaxLifetime != "" {
		if duration, err := time.ParseDuration(cfg.ConnMaxLifetime); err == nil {
			pool.ConnMaxLifetime = duration
		} else {
			log.Printf("ignoring invalid database.conn_max_lifetime=%q", cfg.ConnMaxLifetime)
		}
	}
	if cfg.ConnMaxIdleTime != "" {
		if duration, err := time.ParseDuration(cfg.ConnMaxIdleTime); err == nil {
			pool.ConnMaxIdleTime = duration
		} else {
			log.Printf("ignoring invalid database.conn_max_idle_time=%q", cfg.ConnMaxIdleTime)
		}
	}
	return pool
}

func buildPostgresPoolConfig(cfg DBConfig) postgres.PoolConfig {
	pool := postgres.DefaultPoolConfig()
	if cfg.MaxOpenConns > 0 {
		pool.MaxOpenConns = cfg.MaxOpenConns
	}
	if cfg.MaxIdleConns > 0 {
		pool.MaxIdleConns = cfg.MaxIdleConns
	}
	if pool.MaxOpenConns > 0 && pool.MaxIdleConns > pool.MaxOpenConns {
		pool.MaxIdleConns = pool.MaxOpenConns
	}
	if cfg.ConnMaxLifetime != "" {
		if duration, err := time.ParseDuration(cfg.ConnMaxLifetime); err == nil {
			pool.ConnMaxLifetime = duration
		} else {
			log.Printf("ignoring invalid database.conn_max_lifetime=%q", cfg.ConnMaxLifetime)
		}
	}
	if cfg.ConnMaxIdleTime != "" {
		if duration, err := time.ParseDuration(cfg.ConnMaxIdleTime); err == nil {
			pool.ConnMaxIdleTime = duration
		} else {
			log.Printf("ignoring invalid database.conn_max_idle_time=%q", cfg.ConnMaxIdleTime)
		}
	}
	return pool
}

func openStore(cfg DBConfig) (store.Store, string, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		driver = "mysql"
	}
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, driver, fmt.Errorf("database DSN is required for driver %s", driver)
	}
	switch driver {
	case "mysql":
		db := &mysql.Adapter{}
		pool := buildMySQLPoolConfig(cfg)
		if err := db.OpenWithConfig(dsn, pool); err != nil {
			return nil, driver, err
		}
		log.Printf("database pool configured: driver=%s max_open=%d max_idle=%d conn_max_lifetime=%s conn_max_idle_time=%s",
			driver,
			pool.MaxOpenConns,
			pool.MaxIdleConns,
			pool.ConnMaxLifetime,
			pool.ConnMaxIdleTime,
		)
		return db, driver, nil
	case "postgres", "postgresql", "pg":
		parsed, err := url.Parse(dsn)
		if err != nil || parsed.Scheme == "" {
			return nil, driver, fmt.Errorf("PostgreSQL DSN must be a URL DSN")
		}
		if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
			return nil, driver, fmt.Errorf("PostgreSQL DSN scheme must be postgres or postgresql")
		}
		db := &postgres.Adapter{}
		pool := buildPostgresPoolConfig(cfg)
		if err := db.OpenWithConfig(dsn, pool); err != nil {
			return nil, driver, err
		}
		log.Printf("database pool configured: driver=%s max_open=%d max_idle=%d conn_max_lifetime=%s conn_max_idle_time=%s",
			driver,
			pool.MaxOpenConns,
			pool.MaxIdleConns,
			pool.ConnMaxLifetime,
			pool.ConnMaxIdleTime,
		)
		return db, driver, nil
	default:
		return nil, driver, fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}
}

func chainHTTP(handler http.HandlerFunc, middlewares ...func(http.HandlerFunc) http.HandlerFunc) http.HandlerFunc {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

func useRedisRuntime(cfg RuntimeConfig) bool {
	store := strings.ToLower(strings.TrimSpace(cfg.Store))
	return store == "redis" || (store == "" && strings.TrimSpace(cfg.RedisURL) != "")
}

func openRedisRuntimeState(ctx context.Context, cfg RuntimeConfig) (*server.RedisRuntimeState, error) {
	return server.NewRedisRuntimeState(ctx, server.RedisRuntimeOptions{
		URL:       cfg.RedisURL,
		KeyPrefix: cfg.RedisKeyPrefix,
	})
}

func main() {
	cfgPath := "tinode.conf"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Printf("using default config: %v", err)
		cfg = defaultConfig()
	}

	if err := configureJWTSecret(); err != nil {
		log.Fatal(err)
	}

	// Initialize database
	db, dbDriver, err := openStore(cfg.Database)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer db.Close()

	if err := db.CreateSchema(); err != nil {
		if isProductionEnv() {
			log.Fatalf("schema initialization failed: %v", err)
		}
		log.Printf("schema creation (may already exist): %v", err)
	}
	log.Printf("database initialized: driver=%s", dbDriver)

	// Initialize components
	rateLimiter := server.NewRateLimiter(server.DefaultRateLimits())
	httpLimiter := server.NewHTTPRateLimiter()
	var redisRuntime *server.RedisRuntimeState
	if useRedisRuntime(cfg.Runtime) {
		redisRuntime, err = openRedisRuntimeState(context.Background(), cfg.Runtime)
		if err != nil {
			log.Fatalf("redis runtime initialization failed: %v", err)
		}
		defer redisRuntime.Close()
		log.Printf("runtime state initialized: mode=redis")
	}
	hub := server.NewHub(db, rateLimiter)
	if redisRuntime != nil {
		hub = server.NewHubWithRuntime(db, rateLimiter, redisRuntime, envString("CATSCO_NODE_ID"))
	}
	go hub.Run()

	server.SetBotStats(hub.BotStats())

	// Initialize deployer (optional — only if DEPLOY_API_URL is set)
	var deployer *server.Deployer
	if deployURL := os.Getenv("DEPLOY_API_URL"); deployURL != "" {
		deployer = server.NewDeployer(deployURL)
		log.Printf("Deploy API enabled: %s", deployURL)
	}

	userHandler := server.NewUserHandler(db)
	accountServiceVerifier, err := server.NewAccountServiceVerifier(os.Getenv("OC_ACCOUNT_SERVICE_TOKENS"), db)
	if err != nil {
		log.Fatalf("invalid OC_ACCOUNT_SERVICE_TOKENS: %v", err)
	}
	accountCenterHandler := server.NewAccountCenterHandler(db, accountServiceVerifier)
	accountAdminHandler := server.NewAccountAdminHandler(db, accountServiceVerifier, db)
	friendHandler := server.NewFriendHandler(db)
	conversationHandler := server.NewConversationHandler(db, hub)
	agentHandler := server.NewAgentHandler(db, hub)
	channelAgentBindingHandler := server.NewChannelAgentBindingHandler(db, hub)
	feishuChannelHandler := server.NewFeishuChannelHandlerFromEnv(db, hub)
	weixinChannelHandler := server.NewWeixinChannelHandlerFromEnv(db, hub)
	feishuChannelHandler.InstallOutboundDispatcher()
	weixinChannelHandler.InstallOutboundDispatcher()
	botHandler := server.NewBotHandler(db, deployer)
	botHandler.SetHub(hub)
	msgHandler := server.NewMessageHandler(db, hub)
	deviceHandler := server.NewDeviceHandler(db, hub)
	deviceConnectorHandler := server.NewDeviceConnectorHandler(db, hub)
	uploadHandler := server.NewUploadHandler("./uploads", "/uploads")
	readerHandler := server.NewReaderProxyHandlerFromEnv()
	feedbackHandler := server.NewFeedbackHandler(db)
	relayConfigHandler := server.NewRelayConfigHandler()
	relayKeyHandler := server.NewRelayKeyHandlerFromEnv()
	// usageHandler := server.NewUsageHandler(db)

	authSendCodeIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "auth_send_code_ip", Limit: 20, Window: time.Minute, Burst: 5,
	})
	authSendCodeEmailLimit := httpLimiter.LimitJSONField(server.HTTPRateLimitConfig{
		Name: "auth_send_code_email", Limit: 3, Window: 10 * time.Minute, Burst: 3,
	}, "email")
	authResetCodeIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "auth_reset_code_ip", Limit: 10, Window: time.Minute, Burst: 3,
	})
	authResetCodeEmailLimit := httpLimiter.LimitJSONField(server.HTTPRateLimitConfig{
		Name: "auth_reset_code_email", Limit: 3, Window: 10 * time.Minute, Burst: 3,
	}, "email")
	authResetPasswordIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "auth_reset_password_ip", Limit: 20, Window: time.Minute, Burst: 5,
	})
	authResetPasswordEmailLimit := httpLimiter.LimitJSONField(server.HTTPRateLimitConfig{
		Name: "auth_reset_password_email", Limit: 5, Window: 10 * time.Minute, Burst: 3,
	}, "email")
	authLoginIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "auth_login_ip", Limit: 60, Window: time.Minute, Burst: 10,
	})
	authLoginAccountLimit := httpLimiter.LimitJSONField(server.HTTPRateLimitConfig{
		Name: "auth_login_account", Limit: 10, Window: 10 * time.Minute, Burst: 5,
	}, "account")
	authRegisterIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "auth_register_ip", Limit: 10, Window: time.Hour, Burst: 3,
	})
	authRegisterEmailLimit := httpLimiter.LimitJSONField(server.HTTPRateLimitConfig{
		Name: "auth_register_email", Limit: 5, Window: time.Hour, Burst: 3,
	}, "email")
	authRegisterUsernameLimit := httpLimiter.LimitJSONField(server.HTTPRateLimitConfig{
		Name: "auth_register_username", Limit: 5, Window: time.Hour, Burst: 3,
	}, "username")
	uploadIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "upload_ip", Limit: 60, Window: time.Minute, Burst: 20,
	})
	uploadUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "upload_user", Limit: 30, Window: time.Minute, Burst: 10,
	})
	readerIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "reader_ip", Limit: 20, Window: time.Minute, Burst: 5,
	})
	readerUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "reader_user", Limit: 10, Window: time.Minute, Burst: 3,
	})
	feedbackIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "feedback_ip", Limit: 20, Window: time.Minute, Burst: 5,
	})
	feedbackUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "feedback_user", Limit: 10, Window: 10 * time.Minute, Burst: 3,
	})
	deviceConnectorEnrollIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "device_connector_enroll_ip", Limit: 20, Window: time.Minute, Burst: 5,
	})

	// HTTP routes
	mux := http.NewServeMux()

	// Health check endpoints (no auth required)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		health := db.HealthCheck()
		if health["status"] == "healthy" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(health)
	})

	// Auth
	mux.HandleFunc("/api/auth/send-code", chainHTTP(userHandler.HandleSendCode, authSendCodeIPLimit, authSendCodeEmailLimit))
	mux.HandleFunc("/api/auth/reset-password/send-code", chainHTTP(userHandler.HandleResetPasswordSendCode, authResetCodeIPLimit, authResetCodeEmailLimit))
	mux.HandleFunc("/api/auth/reset-password", chainHTTP(userHandler.HandleResetPassword, authResetPasswordIPLimit, authResetPasswordEmailLimit))
	mux.HandleFunc("/api/auth/register", chainHTTP(userHandler.HandleRegister, authRegisterIPLimit, authRegisterEmailLimit, authRegisterUsernameLimit))
	mux.HandleFunc("/api/auth/login", chainHTTP(userHandler.HandleLogin, authLoginIPLimit, authLoginAccountLimit))

	// Account center (service-to-service auth)
	mux.HandleFunc("/api/account/introspect", accountCenterHandler.HandleIntrospect)
	mux.HandleFunc("/api/account/users/", accountCenterHandler.HandleGetUser)
	mux.HandleFunc("/local/account-admin", accountAdminHandler.HandlePage)
	mux.HandleFunc("/local/account-admin/", accountAdminHandler.HandlePage)
	mux.HandleFunc("/local/account-admin/users", accountAdminHandler.HandleUserLookup)
	mux.HandleFunc("/local/account-admin/users/list", accountAdminHandler.HandleUserList)
	mux.HandleFunc("/local/account-admin/users/search", accountAdminHandler.HandleUserSearch)
	mux.HandleFunc("/local/account-admin/users/state", accountAdminHandler.HandleUserState)
	mux.HandleFunc("/local/account-admin/services", accountAdminHandler.HandleServices)
	mux.HandleFunc("/local/account-admin/services/revoke", accountAdminHandler.HandleRevokeService)

	// Friends (require auth — JWT or API Key for bot access)
	authWithDB := server.AuthMiddlewareWithDB(db)
	jwtAuthWithDB := server.JWTAuthMiddlewareWithDB(db)
	ownerAuthWithDB := server.OwnerMiddlewareWithDB(db)
	adminAuthWithDB := server.AdminMiddlewareWithDB(db)
	mux.HandleFunc("/api/friends", authWithDB(friendHandler.HandleGetFriends))
	mux.HandleFunc("/api/friends/pending", authWithDB(friendHandler.HandleGetPendingRequests))
	mux.HandleFunc("/api/friends/request", authWithDB(friendHandler.HandleSendRequest))
	mux.HandleFunc("/api/friends/accept", authWithDB(friendHandler.HandleAcceptRequest))
	mux.HandleFunc("/api/friends/reject", authWithDB(friendHandler.HandleRejectRequest))
	mux.HandleFunc("/api/friends/block", authWithDB(friendHandler.HandleBlock))
	mux.HandleFunc("/api/friends/remove", authWithDB(friendHandler.HandleRemoveFriend))

	// User search
	mux.HandleFunc("/api/users/search", authWithDB(friendHandler.HandleSearchUsers))

	// User profile (require auth — JWT or API Key)
	mux.HandleFunc("/api/me", authWithDB(userHandler.HandleMe))
	mux.HandleFunc("/api/me/update", jwtAuthWithDB(userHandler.HandleUpdateMe))

	// Messages (require auth — JWT or API Key for bot access)
	mux.HandleFunc("/api/messages/send", authWithDB(msgHandler.HandleSendMessage))
	mux.HandleFunc("/api/messages", authWithDB(msgHandler.HandleGetMessages))
	mux.HandleFunc("/api/conversations", authWithDB(conversationHandler.HandleList))
	mux.HandleFunc("/api/agents", jwtAuthWithDB(agentHandler.HandleListAgents))
	mux.HandleFunc("/api/agents/open", jwtAuthWithDB(agentHandler.HandleOpenAgent))
	mux.HandleFunc("/api/agent-entries", ownerAuthWithDB(channelAgentBindingHandler.HandleAgentEntries))
	mux.HandleFunc("/api/agent-entries/", ownerAuthWithDB(channelAgentBindingHandler.HandleAgentEntryByID))
	mux.HandleFunc("/api/channel-agent-entry/preview", channelAgentBindingHandler.HandleAgentEntryPreview)
	mux.HandleFunc("/api/channel-agent-bindings/confirm", channelAgentBindingHandler.HandleConfirmChannelAgentBinding)
	mux.HandleFunc("/api/channel-agent-bindings/resolve", channelAgentBindingHandler.HandleResolveChannelAgentBinding)
	mux.HandleFunc("/api/channel-agent-bindings/link-user", jwtAuthWithDB(channelAgentBindingHandler.HandleLinkChannelAgentBindingUser))
	mux.HandleFunc("/api/channel-agent-bindings/oauth/feishu/start", feishuChannelHandler.HandleOAuthStart)
	mux.HandleFunc("/api/channel-agent-bindings/oauth/feishu/callback", feishuChannelHandler.HandleOAuthCallback)
	mux.HandleFunc("/api/channels/feishu/events", feishuChannelHandler.HandleEvents)
	mux.HandleFunc("/api/channel-agent-entry/weixin-qrcode", weixinChannelHandler.HandleQRCode)
	mux.HandleFunc("/api/channels/weixin/events", weixinChannelHandler.HandleEvents)
	mux.HandleFunc("/api/feedback", chainHTTP(feedbackHandler.HandleCreateFeedback, feedbackIPLimit, authWithDB, feedbackUserLimit))
	mux.HandleFunc("/api/relay/config", ownerAuthWithDB(relayConfigHandler.HandleConfig))
	mux.HandleFunc("/api/relay/session", ownerAuthWithDB(relayConfigHandler.HandleSession))
	mux.HandleFunc("/api/relay/key", ownerAuthWithDB(relayKeyHandler.HandleKey))
	mux.HandleFunc("/api/relay/key/rotate", ownerAuthWithDB(relayKeyHandler.HandleRotate))
	mux.HandleFunc("/api/relay/key/reveal", ownerAuthWithDB(relayKeyHandler.HandleReveal))
	mux.HandleFunc("/api/devices", authWithDB(deviceHandler.HandleListDevices))
	mux.HandleFunc("/api/devices/", jwtAuthWithDB(deviceHandler.HandleDeviceByID))
	mux.HandleFunc("/api/devices/register", authWithDB(deviceHandler.HandleRegisterDevice))
	mux.HandleFunc("/api/devices/rpc-status", jwtAuthWithDB(deviceHandler.HandleDeviceRPCStatus))
	mux.HandleFunc("/api/devices/audit", jwtAuthWithDB(deviceConnectorHandler.HandleAudit))
	mux.HandleFunc("/api/device-connectors/pairings", ownerAuthWithDB(deviceConnectorHandler.HandleCreatePairing))
	mux.HandleFunc("/api/device-connectors/pairings/", ownerAuthWithDB(deviceConnectorHandler.HandlePairingByID))
	mux.HandleFunc("/api/device-connectors/enroll", chainHTTP(deviceConnectorHandler.HandleEnroll, deviceConnectorEnrollIPLimit))
	mux.HandleFunc("/api/device-connectors/token/refresh", deviceConnectorHandler.HandleRefreshToken)
	mux.HandleFunc("/api/device-connectors/register", deviceConnectorHandler.HandleRegisterDevice)
	mux.HandleFunc("/api/device-connectors/releases", deviceConnectorHandler.HandleReleases)

	// Online status API
	mux.HandleFunc("/api/users/online", jwtAuthWithDB(func(w http.ResponseWriter, r *http.Request) {
		uid := server.UIDFromContext(r.Context())
		onlineList, err := server.BuildOnlineStatusList(db, hub, uid)
		if err != nil {
			server.WriteJSONPublic(w, http.StatusInternalServerError, map[string]string{"error": "failed"})
			return
		}
		server.WriteJSONPublic(w, http.StatusOK, map[string]interface{}{"users": onlineList})
	}))

	// Bot management (admin — legacy)
	mux.HandleFunc("/api/admin/bots", adminAuthWithDB(botHandler.HandleListBots))
	mux.HandleFunc("/api/admin/bots/register", adminAuthWithDB(botHandler.HandleRegisterBot))
	mux.HandleFunc("/api/admin/bots/toggle", adminAuthWithDB(botHandler.HandleToggleBot))
	mux.HandleFunc("/api/admin/bots/rotate-key", adminAuthWithDB(botHandler.HandleRotateAPIKey))
	mux.HandleFunc("/api/admin/bots/stats", adminAuthWithDB(botHandler.HandleBotStats))
	mux.HandleFunc("/api/admin/bots/debug", adminAuthWithDB(botHandler.HandleBotDebugLog))

	// Bot management (user-facing — owner creates/manages their bots)
	mux.HandleFunc("/api/bots", ownerAuthWithDB(botHandler.HandleBotsRouter))
	mux.HandleFunc("/api/bots/deploy", ownerAuthWithDB(botHandler.HandleDeployBot))
	mux.HandleFunc("/api/bots/api-key", ownerAuthWithDB(botHandler.HandleGetBotAPIKey))
	mux.HandleFunc("/api/bots/body-status", ownerAuthWithDB(botHandler.HandleGetBotBodyStatus))
	mux.HandleFunc("/api/bots/visibility", ownerAuthWithDB(botHandler.HandleSetBotVisibility))
	mux.HandleFunc("/api/bots/avatar", ownerAuthWithDB(botHandler.HandleUpdateBotAvatar))
	mux.HandleFunc("/api/bots/friends", ownerAuthWithDB(botHandler.HandleGetBotFriends))

	// Groups (require auth)
	groupHandler := server.NewGroupHandler(db, hub)
	mux.HandleFunc("/api/groups", jwtAuthWithDB(groupHandler.HandleGetGroups))
	mux.HandleFunc("/api/groups/create", jwtAuthWithDB(groupHandler.HandleCreateGroup))
	mux.HandleFunc("/api/groups/info", jwtAuthWithDB(groupHandler.HandleGetGroupInfo))
	mux.HandleFunc("/api/groups/update", jwtAuthWithDB(groupHandler.HandleUpdateGroup))
	mux.HandleFunc("/api/groups/invite", jwtAuthWithDB(groupHandler.HandleInviteMembers))
	mux.HandleFunc("/api/groups/leave", jwtAuthWithDB(groupHandler.HandleLeaveGroup))
	mux.HandleFunc("/api/groups/kick", jwtAuthWithDB(groupHandler.HandleKickMember))
	mux.HandleFunc("/api/groups/mute", jwtAuthWithDB(groupHandler.HandleMuteMember))
	mux.HandleFunc("/api/groups/unmute", jwtAuthWithDB(groupHandler.HandleUnmuteMember))
	mux.HandleFunc("/api/groups/announcement", jwtAuthWithDB(groupHandler.HandleSetAnnouncement))
	mux.HandleFunc("/api/groups/disband", jwtAuthWithDB(groupHandler.HandleDisbandGroup))
	mux.HandleFunc("/api/groups/role", jwtAuthWithDB(groupHandler.HandleUpdateRole))

	// File upload (accepts both JWT and API Key for bot uploads)
	mux.HandleFunc("/api/upload", chainHTTP(uploadHandler.HandleUpload, uploadIPLimit, authWithDB, uploadUserLimit))
	mux.HandleFunc("/api/reader/analyze", chainHTTP(readerHandler.HandleAnalyze, readerIPLimit, authWithDB, readerUserLimit))
	mux.HandleFunc("/uploads/", uploadHandler.HandleServeFile)

	if err := readerHandler.ConfigError(); err != nil {
		log.Printf("Reader proxy is unavailable until configured: %v", err)
	}

	// Token usage tracking (API Key auth for bots)
	// mux.HandleFunc("/api/v1/usage/report", authWithDB(usageHandler.HandleReportUsage))
	// mux.HandleFunc("/api/v1/usage", authWithDB(usageHandler.HandleGetUsage))

	// WebSocket
	mux.HandleFunc(cfg.WebSocket.Path, func(w http.ResponseWriter, r *http.Request) {
		server.ServeWS(hub, w, r)
	})

	// Static files
	if cfg.Static.Dir != "" {
		fs := http.FileServer(http.Dir(cfg.Static.Dir))
		mux.HandleFunc("/e/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				server.WriteJSONPublic(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			http.ServeFile(w, r, filepath.Join(cfg.Static.Dir, "index.html"))
		})
		mux.Handle("/", fs)
	}

	// Start HTTP server
	// Note: no ReadTimeout/WriteTimeout here — WebSocket connections are long-lived.
	// The WS pump handles its own deadlines (writeWait, pongWait).
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.CORSMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	// Start gRPC server
	grpcServer := grpc.NewServer()
	go func() {
		lis, err := net.Listen("tcp", cfg.GRPCPort)
		if err != nil {
			log.Fatalf("gRPC listen failed: %v", err)
		}
		log.Printf("gRPC server listening on %s", cfg.GRPCPort)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("gRPC serve failed: %v", err)
		}
	}()

	// Start HTTP server
	go func() {
		log.Printf("HTTP server listening on %s", cfg.Listen)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop accepting new connections
	log.Println("stopping gRPC server...")
	grpcServer.GracefulStop()

	log.Println("stopping HTTP server...")
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	// Close WebSocket connections gracefully
	log.Println("closing WebSocket connections...")
	// hub.Shutdown()

	log.Println("server stopped")
}
