// Command server is the OpenRMM control plane: REST API, agent WebSocket, remote
// relay, and the AI chat engine.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// migrateOnly runs the pending migrations and exits.
//
// It exists so the credential that can change the schema never has to live in
// the process that serves HTTP. Applying migrations needs ownership of the
// tables; serving does not — and the audit log's append-only guarantee rests on
// the serving role being unable to delete from it, which it cannot be if it also
// owns the table.
//
// So the deployment runs this first, with the owner's credentials, and then
// starts the server with the restricted ones. In Kubernetes that is an init
// container or a Job; in Compose, a one-shot service. Either way the owner
// credential exists for a few seconds and is gone.
var migrateOnly = flag.Bool("migrate-only", false,
	"apply pending database migrations and exit, without starting the server")

// version is the release this binary was built from, stamped at build time:
//
//	go build -ldflags "-X main.version=0.6.2" ./cmd/server
//
// The Dockerfile (build arg VERSION, set by the image workflow from the tag)
// and `make server` stamp it. An unstamped build says "dev" rather than a
// number that looks real: the console showed a hardcoded "0.2.1" for months.
var version = "dev"

var showVersion = flag.Bool("version", false, "print the version this binary was built from and exit")

func main() {
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg.Version = version

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
		// A permission failure here means the serving role cannot change the
		// schema — which is the intended arrangement, not a misconfiguration.
		// Saying so turns a confusing deploy failure into an instruction.
		if isPermissionError(err) && !*migrateOnly {
			log.Fatalf("database migrations failed: %v\n\n"+
				"This role cannot change the schema, which is expected when the server runs as a\n"+
				"restricted user. Run the migrations first with the owner's credentials:\n"+
				"    RMM_DATABASE_URL=<owner dsn> rmm-server -migrate-only\n"+
				"then start the server again.", err)
		}
		log.Fatalf("database migrations failed: %v", err)
	}
	migrationCancel()
	log.Println("database migrations complete")
	if *migrateOnly {
		// Nothing else is touched: no settings are read, no listener opened, no
		// scheduler started. The point is to hold the owner credential for the
		// shortest possible time.
		log.Println("migrate-only: done")
		return
	}
	if eff, err := settings.Effective(ctx, st, cfg); err != nil {
		log.Fatalf("settings: %v", err)
	} else {
		eff.ApplyToConfig(cfg)
	}
	log.Println("database schema is up to date")

	// Say plainly which audit protections are actually in force. Two of the three
	// layers are silent when missing: the triggers refuse a write and the hash
	// chain reports a break, but privilege separation announces nothing — a
	// server connected as the table owner behaves identically to one that cannot
	// delete, right until something with those credentials does. Nobody
	// discovers that from behaviour, so it is said at every start.
	if status, err := st.AuditProtection(ctx); err != nil {
		log.Printf("audit: could not determine protection status: %v", err)
	} else if !status.TriggersInstalled {
		log.Printf("audit: WARNING append-only triggers are not installed on audit_log")
	} else if status.CanDeleteDirectly || status.Superuser {
		log.Printf("audit: append-only triggers active, but this connection (role %q%s) "+
			"can bypass them. Grant openrmm_app a login and point RMM_DATABASE_URL at it "+
			"to make a compromised server unable to erase the trail. See docs/07.",
			status.Role, superSuffix(status.Superuser))
	} else {
		log.Printf("audit: append-only enforced; role %q cannot delete or modify the trail", status.Role)
	}

	h := hub.New()
	jobSvc := jobs.New(st, h)
	remoteMgr := remote.NewManager(st, h, cfg.MeshCentralURL, cfg.MeshCentralTokenKey, cfg.MeshCentralUser, cfg.MeshCentralEnabled, cfg.RustDeskEnabled)
	relay := remote.NewRelay()
	aiClient := ai.NewClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	patchSvc := patches.New(st, h)
	chat := ai.NewEngine(aiClient, st, jobSvc, patchSvc, cfg)
	// SMTP is resolved per use, not captured here: settings are DB-backed and an
	// operator fixing a rejected password must not need a restart.
	// Alerts leave through the tenant's own alerts profile, never the account
	// transport: with no alerts server configured, alert mail is off.
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
	// Discovery talks to the identity provider over the network, so it happens
	// here rather than in New: a provider that is down delays this call, and it
	// must not delay building the server or block the agent hub. It logs and
	// carries on — local sign-in does not depend on it.
	srv.InitOIDC(ctx)
	// After discovery: the device limit comes from the identity provider's
	// entitlement endpoint, when it names one (OpenRMM Cloud).
	go srv.RunDeviceLimit(schedCtx)
	go srv.RunSupportLink(schedCtx)

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

// superSuffix names the reason a role can bypass the triggers, since "superuser"
// and "owns the table" need different fixes.
func superSuffix(super bool) string {
	if super {
		return ", a superuser"
	}
	return ""
}

// isPermissionError reports whether a migration failed because the role lacks
// the rights, rather than because the migration itself is wrong.
func isPermissionError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// 42501 insufficient_privilege, 42P01 undefined_table — the latter shows
		// up when a restricted role cannot even see what it is not allowed to
		// create.
		return pgErr.Code == "42501" || pgErr.Code == "42P01"
	}
	return strings.Contains(strings.ToLower(err.Error()), "permission denied")
}
