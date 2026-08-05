package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// readyz is a readiness probe distinct from /healthz (liveness). It reports the
// process can serve traffic — specifically that the database is reachable — so an
// orchestrator can gate routing on it. It returns 503 while a dependency is down.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, "database unavailable: %v\n", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready\n"))
}

// metrics exposes a minimal Prometheus text-format snapshot. It is disabled
// unless RMM_METRICS_TOKEN is set, and then requires that token as a bearer so a
// scraper behind the edge can read it without exposing fleet size publicly. It
// intentionally avoids a metrics-client dependency: the handful of gauges here
// (DB pool, online agents, goroutines) are the operational signals we lack today.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.MetricsToken
	if token == "" {
		http.NotFound(w, r)
		return
	}
	if !bearerEquals(r.Header.Get("Authorization"), token) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var b strings.Builder
	writeGauge := func(name, help string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, value)
	}

	writeGauge("rmm_agents_online", "Agents with a live WebSocket connection.", float64(s.hub.OnlineCount()))
	writeGauge("rmm_goroutines", "Number of goroutines in the server process.", float64(runtime.NumGoroutine()))

	if st := s.store.PoolStat(); st != nil {
		writeGauge("rmm_db_pool_total_conns", "Total database connections in the pool.", float64(st.TotalConns()))
		writeGauge("rmm_db_pool_idle_conns", "Idle database connections in the pool.", float64(st.IdleConns()))
		writeGauge("rmm_db_pool_acquired_conns", "Acquired (in-use) database connections.", float64(st.AcquiredConns()))
	}

	// The outbox. Alert delivery failing is the failure that hides every other
	// failure — you notice it by not being told about anything, which is to say
	// you do not notice it. Age of the oldest pending message is the one to
	// alert on: a healthy outbox drains within a worker tick (30s), so minutes
	// of backlog means mail has stopped leaving.
	outboxCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if outbox, err := s.store.OutboxStats(outboxCtx); err == nil {
		writeGauge("rmm_notifications_pending", "Queued alert messages not yet delivered.", float64(outbox.Pending))
		writeGauge("rmm_notifications_failed", "Alert messages abandoned after the retry limit.", float64(outbox.Failed))
		writeGauge("rmm_notifications_oldest_pending_seconds", "Age of the oldest undelivered message; 0 when the outbox is empty.", outbox.OldestPendingAgeSec)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// bearerEquals reports whether an Authorization header carries the expected
// bearer token, compared in constant time.
func bearerEquals(header, expected string) bool {
	const prefix = "Bearer "
	if expected == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
