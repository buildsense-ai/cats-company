package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/openchat/openchat/server"
	"github.com/openchat/openchat/server/db/mysql"
)

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

	// JWT secret from env
	if secret := os.Getenv("OC_JWT_SECRET"); secret != "" {
		server.SetJWTSecret(secret)
	}

	// Initialize database
	db := &mysql.Adapter{}
	if err := db.Open(cfg.Database.DSN); err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer db.Close()

	if err := db.CreateSchema(); err != nil {
		log.Printf("schema creation (may already exist): %v", err)
	}

	// Initialize components
	rateLimiter := server.NewRateLimiter(server.DefaultRateLimits())
	hub := server.NewHub(db, rateLimiter)
	go hub.Run()

	server.SetBotStats(hub.BotStats())

	// Initialize deployer (optional — only if DEPLOY_API_URL is set)
	var deployer *server.Deployer
	if deployURL := os.Getenv("DEPLOY_API_URL"); deployURL != "" {
		deployer = server.NewDeployer(deployURL)
		log.Printf("Deploy API enabled: %s", deployURL)
	}

	userHandler := server.NewUserHandler(db)
	friendHandler := server.NewFriendHandler(db)
	botHandler := server.NewBotHandler(db, deployer)
	msgHandler := server.NewMessageHandler(db)
	uploadHandler := server.NewUploadHandler("./uploads", "/uploads")

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
	mux.HandleFunc("/api/auth/register", userHandler.HandleRegister)
	mux.HandleFunc("/api/auth/login", userHandler.HandleLogin)

	// Friends (require auth — JWT or API Key for bot access)
	authWithDB := server.AuthMiddlewareWithDB(db)
	mux.HandleFunc("/api/friends", authWithDB(friendHandler.HandleGetFriends))
	mux.HandleFunc("/api/friends/pending", authWithDB(friendHandler.HandleGetPendingRequests))
	mux.HandleFunc("/api/friends/request", authWithDB(friendHandler.HandleSendRequest))
	mux.HandleFunc("/api/friends/accept", authWithDB(friendHandler.HandleAcceptRequest))
	mux.HandleFunc("/api/friends/reject", authWithDB(friendHandler.HandleRejectRequest))
	mux.HandleFunc("/api/friends/block", authWithDB(friendHandler.HandleBlock))
	mux.HandleFunc("/api/friends/remove", authWithDB(friendHandler.HandleRemoveFriend))

	// User search
	mux.HandleFunc("/api/users/search", friendHandler.HandleSearchUsers)

	// User profile (require auth — JWT or API Key)
	mux.HandleFunc("/api/me", authWithDB(userHandler.HandleMe))

	// Messages (require auth) — kept for REST fallback
	mux.HandleFunc("/api/messages/send", server.AuthMiddleware(msgHandler.HandleSendMessage))
	mux.HandleFunc("/api/messages", server.AuthMiddleware(msgHandler.HandleGetMessages))

	// Online status API
	mux.HandleFunc("/api/users/online", server.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		uid := server.UIDFromContext(r.Context())
		friends, err := db.GetFriends(uid)
		if err != nil {
			server.WriteJSONPublic(w, http.StatusInternalServerError, map[string]string{"error": "failed"})
			return
		}
		result := make([]map[string]interface{}, 0, len(friends))
		for _, f := range friends {
			result = append(result, map[string]interface{}{
				"uid":    f.ID,
				"online": hub.IsOnline(f.ID),
			})
		}
		server.WriteJSONPublic(w, http.StatusOK, map[string]interface{}{"users": result})
	}))

	// Bot management (admin — legacy)
	mux.HandleFunc("/api/admin/bots", botHandler.HandleListBots)
	mux.HandleFunc("/api/admin/bots/register", botHandler.HandleRegisterBot)
	mux.HandleFunc("/api/admin/bots/toggle", botHandler.HandleToggleBot)
	mux.HandleFunc("/api/admin/bots/rotate-key", botHandler.HandleRotateAPIKey)
	mux.HandleFunc("/api/admin/bots/stats", botHandler.HandleBotStats)
	mux.HandleFunc("/api/admin/bots/debug", botHandler.HandleBotDebugLog)

	// Bot management (user-facing — owner creates/manages their bots)
	mux.HandleFunc("/api/bots", authWithDB(botHandler.HandleBotsRouter))
	mux.HandleFunc("/api/bots/deploy", authWithDB(botHandler.HandleDeployBot))
	mux.HandleFunc("/api/bots/visibility", authWithDB(botHandler.HandleSetBotVisibility))

	// Groups (require auth)
	groupHandler := server.NewGroupHandler(db, hub)
	mux.HandleFunc("/api/groups", server.AuthMiddleware(groupHandler.HandleGetGroups))
	mux.HandleFunc("/api/groups/create", server.AuthMiddleware(groupHandler.HandleCreateGroup))
	mux.HandleFunc("/api/groups/info", server.AuthMiddleware(groupHandler.HandleGetGroupInfo))
	mux.HandleFunc("/api/groups/invite", server.AuthMiddleware(groupHandler.HandleInviteMembers))
	mux.HandleFunc("/api/groups/leave", server.AuthMiddleware(groupHandler.HandleLeaveGroup))
	mux.HandleFunc("/api/groups/kick", server.AuthMiddleware(groupHandler.HandleKickMember))
	mux.HandleFunc("/api/groups/mute", server.AuthMiddleware(groupHandler.HandleMuteMember))
	mux.HandleFunc("/api/groups/unmute", server.AuthMiddleware(groupHandler.HandleUnmuteMember))
	mux.HandleFunc("/api/groups/announcement", server.AuthMiddleware(groupHandler.HandleSetAnnouncement))
	mux.HandleFunc("/api/groups/disband", server.AuthMiddleware(groupHandler.HandleDisbandGroup))
	mux.HandleFunc("/api/groups/role", server.AuthMiddleware(groupHandler.HandleUpdateRole))

	// File upload (accepts both JWT and API Key for bot uploads)
	mux.HandleFunc("/api/upload", authWithDB(uploadHandler.HandleUpload))
	mux.HandleFunc("/uploads/", uploadHandler.HandleServeFile)

	// WebSocket
	mux.HandleFunc(cfg.WebSocket.Path, func(w http.ResponseWriter, r *http.Request) {
		server.ServeWS(hub, w, r)
	})

	// Static files
	if cfg.Static.Dir != "" {
		fs := http.FileServer(http.Dir(cfg.Static.Dir))
		mux.Handle("/", fs)
	}

	// Start HTTP server
	// Note: no ReadTimeout/WriteTimeout here — WebSocket connections are long-lived.
	// The WS pump handles its own deadlines (writeWait, pongWait).
	httpServer := &http.Server{
		Addr:    cfg.Listen,
		Handler: mux,
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
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	grpcServer.GracefulStop()
	httpServer.Shutdown(ctx)
	log.Println("server stopped")
}
