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

func envBool(name string) bool {
	switch strings.ToLower(envString(name)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func envInt64Set(name string) map[int64]bool {
	out := map[int64]bool{}
	raw := envString(name)
	if raw == "" {
		return out
	}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	}) {
		var uid int64
		if _, err := fmt.Sscan(strings.TrimSpace(item), &uid); err == nil && uid > 0 {
			out[uid] = true
		}
	}
	return out
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

func limitHTTPMethod(method string, middleware func(http.HandlerFunc) http.HandlerFunc) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		limited := middleware(next)
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == method {
				limited(w, r)
				return
			}
			next(w, r)
		}
	}
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

	userHandler := server.NewUserHandler(db)
	accountServiceVerifier, err := server.NewAccountServiceVerifier(os.Getenv("OC_ACCOUNT_SERVICE_TOKENS"), db)
	if err != nil {
		log.Fatalf("invalid OC_ACCOUNT_SERVICE_TOKENS: %v", err)
	}
	var commercialStore server.CommercialStore
	if candidate, ok := db.(server.CommercialStore); ok {
		commercialStore = candidate
	}
	var commercialPaymentStore server.CommercialPaymentStore
	if candidate, ok := db.(server.CommercialPaymentStore); ok {
		commercialPaymentStore = candidate
	}
	var commercialRelayManagedStore server.CommercialRelayManagedStore
	if candidate, ok := db.(server.CommercialRelayManagedStore); ok {
		commercialRelayManagedStore = candidate
	}
	var commercialOperationsStore server.CommercialOperationsStore
	if candidate, ok := db.(server.CommercialOperationsStore); ok {
		commercialOperationsStore = candidate
	}
	accountCenterHandler := server.NewAccountCenterHandler(db, accountServiceVerifier)
	accountAdminHandler := server.NewAccountAdminHandler(db, accountServiceVerifier, db, commercialStore)
	friendHandler := server.NewFriendHandler(db, hub)
	conversationHandler := server.NewConversationHandler(db, hub)
	projectHandler := server.NewProjectHandler(db)
	agentHandler := server.NewAgentHandler(db, hub)
	channelAgentBindingHandler := server.NewChannelAgentBindingHandler(db, hub)
	feishuChannelHandler := server.NewFeishuChannelHandlerFromEnv(db, hub)
	weixinChannelHandler := server.NewWeixinChannelHandlerFromEnv(db, hub)
	weixinClawBotHandler := server.NewWeixinClawBotHandlerFromEnv(db, hub)
	feishuChannelHandler.InstallOutboundDispatcher()
	weixinChannelHandler.InstallOutboundDispatcher()
	weixinClawBotHandler.InstallOutboundDispatcher()
	weixinClawBotHandler.Start()
	defer weixinClawBotHandler.Stop()
	botHandler := server.NewBotHandler(db)
	botHandler.SetHub(hub)
	cloudWorkerHandler := server.NewCloudWorkerHandler(db, botHandler, server.CloudWorkerConfigFromEnv())
	botModelStore, _ := db.(store.BotModelConfigStore)
	botModelConfigHandler := server.NewBotModelConfigHandler(db, botModelStore)
	artifactRuntimeConfigHandler := server.NewArtifactRuntimeConfigHandlerFromEnv()
	botDefinitionStore, _ := db.(store.BotDefinitionStore)
	botDefinitionHandler := server.NewBotDefinitionHandler(db, botDefinitionStore, botModelStore, botModelConfigHandler)
	botDefinitionHandler.SetPromptOnlineResolver(func(uid int64) bool {
		return hub != nil && hub.BotRuntimeOnline(uid)
	})
	skillHubProxyHandler := server.NewSkillHubProxyHandlerFromEnv()
	botModelCloudPublicEnabled := envBool("CATSCO_BOT_MODEL_CLOUD_ENABLED")
	botModelCloudTestUIDs := envInt64Set("CATSCO_BOT_MODEL_CLOUD_TEST_UIDS")
	botModelConfigHandler.SetRollout(botModelCloudPublicEnabled, botModelCloudTestUIDs)
	if botModelCloudPublicEnabled {
		log.Printf("cloud bot model management is enabled for authenticated owners")
	} else if len(botModelCloudTestUIDs) > 0 {
		log.Printf("cloud bot model management allowlist is enabled for %d uid(s)", len(botModelCloudTestUIDs))
	}
	desktopConnectHandler := server.NewDesktopConnectHandler(db)
	msgHandler := server.NewMessageHandler(db, hub)
	pushSubscriptionStore, _ := db.(store.PushSubscriptionStore)
	pushNotificationService := server.NewPushNotificationService(pushSubscriptionStore)
	hub.SetPushNotificationService(pushNotificationService)
	if pushNotificationService.Enabled() {
		log.Printf("web push notifications are enabled")
	} else if err := pushNotificationService.ConfigError(); err != nil {
		log.Printf("web push notifications are disabled: %v", err)
	} else {
		log.Printf("web push notifications are disabled; configure VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, and VAPID_SUBJECT")
	}
	deviceHandler := server.NewDeviceHandler(db, hub)
	deviceConnectorHandler := server.NewDeviceConnectorHandler(db, hub)
	uploadHandler := server.NewUploadHandler("./uploads", "/uploads")
	tutorialTaskHandler := server.NewTutorialTaskHandler("./uploads", "/uploads")
	readerHandler := server.NewReaderProxyHandlerFromEnv()
	cloudArtifactHandler := server.NewCloudArtifactHandlerFromEnv()
	cloudArtifactHandler.SetStore(db)
	hub.SetArtifactContextResolver(cloudArtifactHandler)
	imageGenerationHandler := server.NewImageGenerationProxyHandlerFromEnv()
	sttHandler := server.NewSTTHandlerFromEnv()
	feedbackHandler := server.NewFeedbackHandler(db)
	relayConfigHandler := server.NewRelayConfigHandler()
	relayKeyHandler := server.NewRelayKeyHandlerFromEnv()
	relayKeyHandler.SetCommercialStore(commercialStore)
	relayKeyHandler.SetDeviceModelStatusResolver(func(uid int64) (server.DeviceModelStatus, bool) {
		return server.LatestDeviceModelStatus(hub, uid)
	})
	relayAdminClient := server.NewRelayAdminClientFromEnv()
	userHandler.SetRelayRegistrationProvisioning(relayAdminClient)
	botModelConfigHandler.SetRelayUsageClient(relayAdminClient)
	agentHandler.SetRelayUsageDependencies(relayAdminClient, func(uid int64, bodyID string) (server.DeviceModelStatus, bool) {
		if strings.TrimSpace(bodyID) == "" {
			return server.LatestDeviceModelStatus(hub, uid)
		}
		return server.DeviceModelStatusForBody(hub, uid, bodyID)
	})
	relayCommercialPublicEnabled := envBool("CATS_RELAY_COMMERCIAL_ENABLED")
	relayCommercialTestUIDs := envInt64Set("CATS_RELAY_COMMERCIAL_TEST_UIDS")
	relayCommercialEnforceEnabled := envBool("CATS_RELAY_COMMERCIAL_ENFORCE_ENABLED")
	relayCommercialEnforceUIDs := envInt64Set("CATS_RELAY_COMMERCIAL_ENFORCE_UIDS")
	if relayCommercialPublicEnabled {
		log.Printf("relay commercial package UI is enabled for authenticated owners")
	}
	if len(relayCommercialTestUIDs) > 0 {
		log.Printf("relay commercial package UI allowlist is enabled for %d uid(s)", len(relayCommercialTestUIDs))
	}
	if relayCommercialEnforceEnabled {
		log.Printf("relay commercial enforce sync is enabled")
	}
	if len(relayCommercialEnforceUIDs) > 0 {
		log.Printf("relay commercial enforce sync allowlist is enabled for %d uid(s)", len(relayCommercialEnforceUIDs))
	}
	accountAdminHandler.SetCommercialRelayAdmin(relayAdminClient, relayCommercialEnforceEnabled, relayCommercialEnforceUIDs)
	var commercialRelaySyncer *server.CommercialRelaySyncer
	commercialServiceCtx, commercialServiceCancel := context.WithCancel(context.Background())
	defer commercialServiceCancel()
	if commercialRelayManagedStore != nil && relayAdminClient != nil {
		commercialRelaySyncer = server.NewCommercialRelaySyncer(commercialRelayManagedStore, relayAdminClient, server.CommercialRelaySyncerOptions{
			EnforceEnabled: relayCommercialEnforceEnabled,
			EnforceUIDs:    relayCommercialEnforceUIDs,
		})
		commercialRelaySyncer.Start(commercialServiceCtx)
		accountAdminHandler.SetCommercialRelaySyncer(commercialRelaySyncer)
	}
	relayCommercialHandler := server.NewRelayCommercialHandlerWithOptions(commercialStore, server.RelayCommercialOptions{
		PublicEnabled:  relayCommercialPublicEnabled,
		TestUIDs:       relayCommercialTestUIDs,
		EnforceEnabled: relayCommercialEnforceEnabled,
		EnforceUIDs:    relayCommercialEnforceUIDs,
		Syncer:         commercialRelaySyncer,
	})
	commercialOpsHandler := server.NewCommercialOpsHandler(accountAdminHandler, accountServiceVerifier, commercialOperationsStore)
	paymentTestUIDs := envInt64Set("CATS_COMMERCIAL_TEST_PAYMENT_UIDS")
	paymentProviders := []server.CommercialPaymentProvider{}
	paymentSaleChannels := map[string]bool{}
	alipaySalesEnabled := envBool("CATS_ALIPAY_SALES_ENABLED")
	if envBool("CATS_COMMERCIAL_TEST_PAYMENT_ENABLED") {
		paymentProviders = append(paymentProviders, server.NewTestCommercialPaymentProvider())
		paymentSaleChannels["test"] = true
		log.Printf("commercial test payment is enabled for %d uid(s)", len(paymentTestUIDs))
	}
	if envBool("CATS_ALIPAY_ENABLED") {
		provider, missing, err := server.NewAlipayPagePaymentProviderFromEnv()
		if err != nil {
			log.Printf("Alipay is disabled due to configuration error: %v", err)
		} else if provider == nil {
			log.Printf("Alipay is waiting for configuration: %s", strings.Join(missing, ", "))
		} else {
			paymentProviders = append(paymentProviders, provider)
			if alipaySalesEnabled {
				paymentSaleChannels["alipay_page"] = true
				log.Printf("Alipay page payment sales are enabled")
			} else {
				log.Printf("Alipay payment provider is enabled for callback and reconciliation only")
			}
		}
	}
	if alipaySalesEnabled && (!relayCommercialEnforceEnabled && len(relayCommercialEnforceUIDs) == 0) {
		log.Printf("Alipay sales are configured, but no uid is eligible until relay commercial enforcement is enabled")
	}
	commercialPaymentHandler := server.NewCommercialPaymentHandler(commercialPaymentStore, server.CommercialPaymentHandlerOptions{
		PublicEnabled: relayCommercialPublicEnabled,
		TestUIDs:      relayCommercialTestUIDs,
		TestPayments:  paymentTestUIDs,
		TrialPlanSlug: envString("CATS_COMMERCIAL_TRIAL_PLAN_SLUG"),
		Providers:     paymentProviders,
		SaleChannels:  paymentSaleChannels,
		Syncer:        commercialRelaySyncer,
	})
	accountAdminHandler.SetCommercialPaymentHandler(commercialPaymentHandler)
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
		// 10 attempts per rolling 5 minutes (was 10 minutes): the shorter window
		// refills tokens twice as fast, so repeated login failures recover sooner
		// and legitimate retries are less likely to trip the limit.
		Name: "auth_login_account", Limit: 10, Window: 5 * time.Minute, Burst: 5,
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
	pushSubscriptionUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "push_subscription_user", Limit: 30, Window: time.Minute, Burst: 10,
	})
	pushTestUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "push_test_user", Limit: 6, Window: time.Minute, Burst: 2,
	})
	readerIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "reader_ip", Limit: 20, Window: time.Minute, Burst: 5,
	})
	readerUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "reader_user", Limit: 10, Window: time.Minute, Burst: 3,
	})
	imageGenerationIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "image_generation_ip", Limit: 20, Window: time.Minute, Burst: 4,
	})
	imageGenerationUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "image_generation_user", Limit: 6, Window: time.Minute, Burst: 2,
	})
	imageTaskPollIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "image_task_poll_ip", Limit: 120, Window: time.Minute, Burst: 30,
	})
	imageTaskPollUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "image_task_poll_user", Limit: 60, Window: time.Minute, Burst: 15,
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
	commercialOrderUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "commercial_order_user", Limit: 12, Window: 10 * time.Minute, Burst: 3,
	})
	commercialOrderCancelUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "commercial_order_cancel_user", Limit: 20, Window: 10 * time.Minute, Burst: 4,
	})
	commercialTrialUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "commercial_trial_user", Limit: 5, Window: time.Hour, Burst: 2,
	})
	commercialTestPaymentUserLimit := httpLimiter.LimitUser(server.HTTPRateLimitConfig{
		Name: "commercial_test_payment_user", Limit: 10, Window: time.Minute, Burst: 3,
	})
	commercialNotifyIPLimit := httpLimiter.LimitIP(server.HTTPRateLimitConfig{
		Name: "commercial_alipay_notify_ip", Limit: 3000, Window: time.Minute, Burst: 500,
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
	mux.HandleFunc("/api/account/commercial-ops/overview", commercialOpsHandler.HandleOverview)
	mux.HandleFunc("/api/account/commercial-ops/plans", commercialOpsHandler.HandlePlans)
	mux.HandleFunc("/api/account/commercial-ops/invites", commercialOpsHandler.HandleInvites)
	mux.HandleFunc("/api/account/commercial-ops/grants", commercialOpsHandler.HandleGrants)
	mux.HandleFunc("/api/account/commercial-ops/users", commercialOpsHandler.HandleUsers)
	mux.HandleFunc("/api/account/commercial-ops/orders", commercialOpsHandler.HandleOrders)
	mux.HandleFunc("/api/account/commercial-ops/order-refunds", commercialOpsHandler.HandleOrderRefund)
	mux.HandleFunc("/api/account/commercial-ops/relay-dry-run", commercialOpsHandler.HandleRelayDryRun)
	mux.HandleFunc("/api/account/commercial-ops/relay-sync", commercialOpsHandler.HandleRelaySync)
	mux.HandleFunc("/local/account-admin", accountAdminHandler.HandlePage)
	mux.HandleFunc("/local/account-admin/", accountAdminHandler.HandlePage)
	mux.HandleFunc("/local/account-admin/users", accountAdminHandler.HandleUserLookup)
	mux.HandleFunc("/local/account-admin/users/list", accountAdminHandler.HandleUserList)
	mux.HandleFunc("/local/account-admin/users/search", accountAdminHandler.HandleUserSearch)
	mux.HandleFunc("/local/account-admin/users/state", accountAdminHandler.HandleUserState)
	mux.HandleFunc("/local/account-admin/services", accountAdminHandler.HandleServices)
	mux.HandleFunc("/local/account-admin/services/revoke", accountAdminHandler.HandleRevokeService)
	mux.HandleFunc("/local/account-admin/commercial/plans", accountAdminHandler.HandleCommercialPlans)
	mux.HandleFunc("/local/account-admin/commercial/invites", accountAdminHandler.HandleCommercialInvites)
	mux.HandleFunc("/local/account-admin/commercial/grants", accountAdminHandler.HandleCommercialGrant)
	mux.HandleFunc("/local/account-admin/commercial/users", accountAdminHandler.HandleCommercialUserSummary)
	mux.HandleFunc("/local/account-admin/commercial/relay-dry-run", accountAdminHandler.HandleCommercialRelayDryRun)
	mux.HandleFunc("/local/account-admin/commercial/relay-sync", accountAdminHandler.HandleCommercialRelaySync)
	mux.HandleFunc("/local/account-admin/commercial/orders", accountAdminHandler.HandleCommercialOrders)
	mux.HandleFunc("/local/account-admin/commercial/order-refunds", accountAdminHandler.HandleCommercialOrderRefund)
	mux.HandleFunc("/local/tutorial-admin", tutorialTaskHandler.HandleAdminPage)
	mux.HandleFunc("/local/tutorial-admin/", tutorialTaskHandler.HandleAdminPage)
	mux.HandleFunc("/local/tutorial-admin/tasks", tutorialTaskHandler.HandleAdminTasks)
	mux.HandleFunc("/local/tutorial-admin/upload", chainHTTP(tutorialTaskHandler.HandleAdminUpload, uploadIPLimit))

	// Friends (require auth — JWT or API Key for bot access)
	authWithDB := server.AuthMiddlewareWithDB(db)
	jwtAuthWithDB := server.JWTAuthMiddlewareWithDB(db)
	ownerAuthWithDB := server.OwnerMiddlewareWithDB(db)
	botAPIKeyAuthWithDB := server.BotAPIKeyMiddlewareWithDB(db)
	adminAuthWithDB := server.AdminMiddlewareWithDB(db)

	// Relay usage admin portal (internal; session-cookie-first + JWT fallback,
	// whitelist-only, guarded proxy)
	relayAdminHandler := server.NewRelayAdminProxyHandler(server.RelayAdminConfigFromEnv(), commercialRelayManagedStore)
	relayAdminAuth := relayAdminHandler.AuthMiddleware(jwtAuthWithDB)
	mux.HandleFunc("/api/admin/relay/access", relayAdminAuth(relayAdminHandler.HandleAccess))
	mux.HandleFunc("/api/admin/relay/", relayAdminAuth(relayAdminHandler.HandleProxy))

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
	mux.HandleFunc("/api/messages/search", authWithDB(msgHandler.HandleSearchMessages))
	mux.HandleFunc("/api/messages", authWithDB(msgHandler.HandleGetMessages))
	mux.HandleFunc("/api/stt/sessions", jwtAuthWithDB(sttHandler.HandleSession))
	mux.HandleFunc("/api/stt/realtime", sttHandler.HandleRealtime)
	mux.HandleFunc("/api/push/config", pushNotificationService.HandleStatus)
	mux.HandleFunc("/api/push/subscriptions", chainHTTP(
		pushNotificationService.HandleSubscription,
		jwtAuthWithDB,
		pushSubscriptionUserLimit,
	))
	mux.HandleFunc("/api/push/test", chainHTTP(
		pushNotificationService.HandleTest,
		jwtAuthWithDB,
		pushTestUserLimit,
	))
	mux.HandleFunc("/api/conversations", authWithDB(conversationHandler.Handle))
	mux.HandleFunc("/api/projects", authWithDB(projectHandler.HandleProjects))
	mux.HandleFunc("/api/projects/topic", authWithDB(projectHandler.HandleProjectTopic))
	mux.HandleFunc("/api/artifacts", jwtAuthWithDB(cloudArtifactHandler.Handle))
	mux.HandleFunc("/api/artifacts/", jwtAuthWithDB(cloudArtifactHandler.Handle))
	mux.HandleFunc("/api/agents", jwtAuthWithDB(agentHandler.HandleListAgents))
	mux.HandleFunc("/api/agents/", jwtAuthWithDB(cloudArtifactHandler.HandleAgentArtifacts))
	mux.HandleFunc("/api/agents/quota", jwtAuthWithDB(agentHandler.HandleAgentQuota))
	mux.HandleFunc("/api/agents/open", jwtAuthWithDB(agentHandler.HandleOpenAgent))
	mux.HandleFunc("GET /api/cloud-workers", jwtAuthWithDB(cloudWorkerHandler.HandleList))
	mux.HandleFunc("POST /api/cloud-workers", jwtAuthWithDB(cloudWorkerHandler.HandleCreate))
	mux.HandleFunc("/api/cloud-workers/", jwtAuthWithDB(cloudWorkerHandler.HandleSub))
	mux.HandleFunc("/api/desktop-connect/session", jwtAuthWithDB(desktopConnectHandler.HandleCreateSession))
	mux.HandleFunc("/api/desktop-connect/exchange", desktopConnectHandler.HandleExchange)
	mux.HandleFunc("/api/desktop-connect/status", desktopConnectHandler.HandleStatus)
	mux.HandleFunc("/api/agent-entries", ownerAuthWithDB(channelAgentBindingHandler.HandleAgentEntries))
	mux.HandleFunc("/api/agent-entries/", ownerAuthWithDB(channelAgentBindingHandler.HandleAgentEntryByID))
	mux.HandleFunc("/api/channel-agent-entry/preview", channelAgentBindingHandler.HandleAgentEntryPreview)
	mux.HandleFunc("/api/channel-agent-bindings", ownerAuthWithDB(channelAgentBindingHandler.HandleListAgentChannelBindings))
	mux.HandleFunc("/api/channel-agent-bindings/confirm", channelAgentBindingHandler.HandleConfirmChannelAgentBinding)
	mux.HandleFunc("/api/channel-agent-bindings/resolve", channelAgentBindingHandler.HandleResolveChannelAgentBinding)
	mux.HandleFunc("/api/channel-agent-bindings/link-user", jwtAuthWithDB(channelAgentBindingHandler.HandleLinkChannelAgentBindingUser))
	mux.HandleFunc("/api/channel-agent-bindings/mobile-link", jwtAuthWithDB(channelAgentBindingHandler.HandleCreateChannelIdentityMobileLink))
	mux.HandleFunc("/api/channel-agent-bindings/group-mobile-link", jwtAuthWithDB(channelAgentBindingHandler.HandleCreateChannelGroupMobileLink))
	mux.HandleFunc("/api/channel-private-bindings", jwtAuthWithDB(channelAgentBindingHandler.HandleChannelPrivateBindings))
	mux.HandleFunc("/api/channel-agent-bindings/weixin-clawbot/qrcode-status", jwtAuthWithDB(weixinClawBotHandler.HandleQRCodeStatus))
	mux.HandleFunc("/api/f/", feishuChannelHandler.HandleOAuthShortLink)
	mux.HandleFunc("/api/fn/", feishuChannelHandler.HandleNativeEntryShortLink)
	mux.HandleFunc("/api/channel-agent-bindings/oauth/feishu/start", feishuChannelHandler.HandleOAuthStart)
	mux.HandleFunc("/api/channel-agent-bindings/oauth/feishu/callback", feishuChannelHandler.HandleOAuthCallback)
	mux.HandleFunc("/api/channels/feishu/events", feishuChannelHandler.HandleEvents)
	mux.HandleFunc("/api/channel-agent-entry/weixin-qrcode", weixinChannelHandler.HandleQRCode)
	mux.HandleFunc("/api/channels/weixin/events", weixinChannelHandler.HandleEvents)
	mux.HandleFunc("/api/feedback", chainHTTP(feedbackHandler.HandleCreateFeedback, feedbackIPLimit, authWithDB, feedbackUserLimit))
	mux.HandleFunc("/api/relay/config", ownerAuthWithDB(relayConfigHandler.HandleConfig))
	mux.HandleFunc("/api/relay/commercial", ownerAuthWithDB(relayCommercialHandler.HandleSummary))
	mux.HandleFunc("/api/relay/invite/redeem", ownerAuthWithDB(relayCommercialHandler.HandleRedeemInvite))
	mux.HandleFunc("/api/relay/commercial/catalog", ownerAuthWithDB(commercialPaymentHandler.HandleCatalog))
	mux.HandleFunc("/api/relay/commercial/orders", chainHTTP(commercialPaymentHandler.HandleOrders, ownerAuthWithDB, limitHTTPMethod(http.MethodPost, commercialOrderUserLimit)))
	mux.HandleFunc("/api/relay/commercial/orders/cancel", chainHTTP(commercialPaymentHandler.HandleCancel, ownerAuthWithDB, commercialOrderCancelUserLimit))
	mux.HandleFunc("/api/relay/commercial/orders/test-confirm", chainHTTP(commercialPaymentHandler.HandleTestConfirm, ownerAuthWithDB, commercialTestPaymentUserLimit))
	mux.HandleFunc("/api/relay/commercial/trial/claim", chainHTTP(commercialPaymentHandler.HandleClaimTrial, ownerAuthWithDB, commercialTrialUserLimit))
	mux.HandleFunc("/api/payments/alipay/notify", commercialNotifyIPLimit(commercialPaymentHandler.HandleAlipayNotify))
	mux.HandleFunc("/api/relay/session", ownerAuthWithDB(relayConfigHandler.HandleSession))
	mux.HandleFunc("/api/relay/key", ownerAuthWithDB(relayKeyHandler.HandleKey))
	mux.HandleFunc("/api/relay/key/rotate", ownerAuthWithDB(relayKeyHandler.HandleRotate))
	mux.HandleFunc("/api/relay/key/reveal", ownerAuthWithDB(relayKeyHandler.HandleReveal))
	mux.HandleFunc("/api/relay/usage", ownerAuthWithDB(relayKeyHandler.HandleUsage))
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
	mux.HandleFunc("/api/catsco/desktop-releases", server.HandleCatsCoDesktopReleases)

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
	mux.HandleFunc("/api/bots/api-key", ownerAuthWithDB(botHandler.HandleGetBotAPIKey))
	mux.HandleFunc("/api/bots/body-status", ownerAuthWithDB(botHandler.HandleGetBotBodyStatus))
	mux.HandleFunc("/api/bots/visibility", ownerAuthWithDB(botHandler.HandleSetBotVisibility))
	mux.HandleFunc("/api/bots/skills-visibility", ownerAuthWithDB(botHandler.HandleSetBotSkillsVisibility))
	mux.HandleFunc("/api/bots/avatar", ownerAuthWithDB(botHandler.HandleUpdateBotAvatar))
	mux.HandleFunc("/api/bots/friends", ownerAuthWithDB(botHandler.HandleGetBotFriends))
	mux.HandleFunc("/api/bots/model-config", ownerAuthWithDB(botModelConfigHandler.HandleOwnerConfig))
	mux.HandleFunc("/api/bot/model-config", botAPIKeyAuthWithDB(botModelConfigHandler.HandleRuntimeConfig))
	mux.HandleFunc("/api/bot/model-config/ack", botAPIKeyAuthWithDB(botModelConfigHandler.HandleRuntimeAck))
	mux.HandleFunc("/api/bot/identity", botAPIKeyAuthWithDB(server.HandleBotIdentity))
	mux.HandleFunc("/api/bot/artifact-runtime-config", botAPIKeyAuthWithDB(artifactRuntimeConfigHandler.Handle))
	mux.HandleFunc("/api/bots/definition", ownerAuthWithDB(botDefinitionHandler.HandleOwnerDefinition))
	mux.HandleFunc("/api/bots/definition/model", ownerAuthWithDB(botDefinitionHandler.HandleOwnerModel))
	mux.HandleFunc("/api/bots/definition/prompt", ownerAuthWithDB(botDefinitionHandler.HandleOwnerPrompt))
	mux.HandleFunc("/api/bots/definition/prompt-visibility", ownerAuthWithDB(botDefinitionHandler.HandleOwnerPromptVisibility))
	mux.HandleFunc("/api/bots/definition/skills", ownerAuthWithDB(botDefinitionHandler.HandleOwnerSkills))
	mux.HandleFunc("/api/agents/prompt", jwtAuthWithDB(botDefinitionHandler.HandleViewerPrompt))
	mux.HandleFunc("/api/agents/skills", jwtAuthWithDB(botDefinitionHandler.HandleViewerSkills))
	mux.HandleFunc("/api/skillhub/skills", jwtAuthWithDB(skillHubProxyHandler.HandleSkills))
	mux.HandleFunc("/api/skillhub/skills/", jwtAuthWithDB(skillHubProxyHandler.HandleSkill))
	mux.HandleFunc("/api/bot/definition", botAPIKeyAuthWithDB(botDefinitionHandler.HandleRuntimeDefinition))
	mux.HandleFunc("/api/bot/definition/skills", botAPIKeyAuthWithDB(botDefinitionHandler.HandleRuntimeSkills))
	mux.HandleFunc("/api/bot/definition/default-prompt", botAPIKeyAuthWithDB(botDefinitionHandler.HandleRuntimeDefaultPrompt))
	mux.HandleFunc("/api/bot/definition/ack", botAPIKeyAuthWithDB(botDefinitionHandler.HandleRuntimeAck))

	// Groups (require auth)
	groupHandler := server.NewGroupHandler(db, hub)
	mux.HandleFunc("/api/groups", jwtAuthWithDB(groupHandler.HandleGetGroups))
	mux.HandleFunc("/api/groups/create", jwtAuthWithDB(groupHandler.HandleCreateGroup))
	mux.HandleFunc("/api/groups/info", jwtAuthWithDB(groupHandler.HandleGetGroupInfo))
	mux.HandleFunc("/api/groups/update", jwtAuthWithDB(groupHandler.HandleUpdateGroup))
	mux.HandleFunc("/api/groups/invite", jwtAuthWithDB(groupHandler.HandleInviteMembers))
	mux.HandleFunc("/api/groups/invite/resolve", jwtAuthWithDB(groupHandler.HandleResolveGroupInviteRequest))
	mux.HandleFunc("/api/groups/leave", jwtAuthWithDB(groupHandler.HandleLeaveGroup))
	mux.HandleFunc("/api/groups/kick", jwtAuthWithDB(groupHandler.HandleKickMember))
	mux.HandleFunc("/api/groups/mute", jwtAuthWithDB(groupHandler.HandleMuteMember))
	mux.HandleFunc("/api/groups/unmute", jwtAuthWithDB(groupHandler.HandleUnmuteMember))
	mux.HandleFunc("/api/groups/announcement", jwtAuthWithDB(groupHandler.HandleSetAnnouncement))
	mux.HandleFunc("/api/groups/disband", jwtAuthWithDB(groupHandler.HandleDisbandGroup))
	mux.HandleFunc("/api/groups/role", jwtAuthWithDB(groupHandler.HandleUpdateRole))

	// File upload (accepts both JWT and API Key for bot uploads)
	mux.HandleFunc("/api/tutorial-tasks", tutorialTaskHandler.HandleTasks)
	mux.HandleFunc("/api/upload", chainHTTP(uploadHandler.HandleUpload, uploadIPLimit, authWithDB, uploadUserLimit))
	mux.HandleFunc("/api/mobile-upload/sessions", chainHTTP(uploadHandler.HandleMobileUploadSession, uploadIPLimit, authWithDB, uploadUserLimit))
	mux.HandleFunc("/api/mobile-upload/sessions/", uploadIPLimit(uploadHandler.HandleMobileUploadSession))
	mux.HandleFunc("/api/reader/analyze", chainHTTP(readerHandler.HandleAnalyze, readerIPLimit, authWithDB, readerUserLimit))
	mux.HandleFunc("/v1/images/generations", chainHTTP(imageGenerationHandler.HandleGenerate, imageGenerationIPLimit, authWithDB, imageGenerationUserLimit))
	mux.HandleFunc("/v1/images/edits", chainHTTP(imageGenerationHandler.HandleEdit, imageGenerationIPLimit, authWithDB, imageGenerationUserLimit))
	mux.HandleFunc("/v1/tasks/", chainHTTP(imageGenerationHandler.HandleTask, imageTaskPollIPLimit, authWithDB, imageTaskPollUserLimit))
	mux.HandleFunc("/uploads/", uploadHandler.HandleServeFile)

	if err := readerHandler.ConfigError(); err != nil {
		log.Printf("Reader proxy is unavailable until configured: %v", err)
	}
	if err := imageGenerationHandler.ConfigError(); err != nil {
		log.Printf("Image generation proxy is unavailable until configured: %v", err)
	}
	if imageGenerationHandler.ConfigError() == nil {
		if err := imageGenerationHandler.EditConfigError(); err != nil {
			log.Printf("Reference-image proxy is unavailable until configured: %v", err)
		}
	}
	if err := sttHandler.ConfigError(); err != nil {
		log.Printf("Streaming STT is unavailable: %v", err)
	} else {
		log.Printf("Streaming STT is enabled with provider %s", server.STTProviderVolcengineDoubaoStreamingV2)
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
		serveSPAIndex := func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				server.WriteJSONPublic(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			http.ServeFile(w, r, filepath.Join(cfg.Static.Dir, "index.html"))
		}
		mux.HandleFunc("/e/", serveSPAIndex)
		mux.HandleFunc("/mobile-upload/", serveSPAIndex)
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
