// Command server is the OpenRMM control plane: REST API, agent WebSocket, remote
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
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/scheduler"
	"github.com/p1ngu/rmm/internal/settings"
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

	const migrationStartupTimeout = 5 * time.Minute
	log.Printf("database migrations starting (startup timeout %s)", migrationStartupTimeout)
	migrationCtx, migrationCancel := context.WithTimeout(ctx, migrationStartupTimeout)
	if err := st.Migrate(migrationCtx); err != nil {
		migrationCancel()
		log.Fatalf("database migrations failed: %v", err)
	}
	migrationCancel()
	log.Println("database migrations complete")
	if eff, err := settings.Effective(ctx, st, cfg); err != nil {
		log.Fatalf("settings: %v", err)
	} else {
		eff.ApplyToConfig(cfg)
	}
	log.Println("database schema is up to date")

	h := hub.New()
	jobSvc := jobs.New(st, h)
	remoteMgr := remote.NewManager(st, h, cfg.MeshCentralURL, cfg.MeshCentralTokenKey, cfg.MeshCentralUser, cfg.MeshCentralEnabled, cfg.RustDeskEnabled)
	relay := remote.NewRelay()
	aiClient := ai.NewClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	patchSvc := patches.New(st, h)
	chat := ai.NewEngine(aiClient, st, jobSvc, patchSvc)
	// SMTP is resolved per use, not captured here: settings are DB-backed and an
	// operator fixing a rejected password must not need a restart.
	// Alerts leave through the alerts profile, which is the account transport
	// unless an operator gave notifications a sender or a relay of their own.
	notifier := notify.New(st, func(ctx context.Context) (notify.Config, string, error) {
		smtpCfg, err := settings.AlertSMTPConfig(ctx, st, cfg)
		return smtpCfg, cfg.PublicURL, err
	})
	jobSvc.WithNotifier(notifier)
	sched := scheduler.New(st, h, jobSvc, patchSvc).
		WithNotifier(notifier, cfg.PublicURL).
		WithRetention(cfg.AlertRetentionDays, cfg.CheckResultRetentionDays, cfg.NotificationRetentionDays).
		WithAuditRetention(cfg.AuditRetentionDays)

	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go sched.Run(schedCtx)
	// The outbox drains in its own loop so a slow SMTP server cannot delay check
	// evaluation for the fleet.
	go notifier.RunWorker(schedCtx)
	log.Println("scheduler started")

	srv := api.New(cfg, st, h, jobSvc, remoteMgr, relay, chat, sched, patchSvc)

	httpServer := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      srv.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0 = no timeout: required for long-lived WebSocket conns
	}

	go func() {
		log.Printf("OpenRMM server listening on %s (public: %s)", cfg.HTTPAddr, cfg.PublicURL)
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
