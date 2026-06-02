// Command server is the RMM-AI control plane: REST API, agent WebSocket, remote
// relay, and the AI chat engine.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/api"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/scheduler"
	"github.com/p1ngu/rmm/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx, store.Schema); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("database schema is up to date")

	h := hub.New()
	jobSvc := jobs.New(st, h)
	remoteMgr := remote.NewManager(st, h, cfg.PublicURL, cfg.MeshCentralURL, cfg.MeshCentralTokenKey, cfg.MeshCentralUser)
	relay := remote.NewRelay()
	aiClient := ai.NewClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	chat := ai.NewEngine(aiClient, st, jobSvc)
	sched := scheduler.New(st, h, jobSvc)

	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go sched.Run(schedCtx)
	log.Println("scheduler started")

	srv := api.New(cfg, st, h, jobSvc, remoteMgr, relay, chat, sched)

	httpServer := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      srv.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 = no timeout: required for long-lived WebSocket conns
	}

	go func() {
		log.Printf("RMM-AI server listening on %s (public: %s)", cfg.HTTPAddr, cfg.PublicURL)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
