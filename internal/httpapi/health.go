package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// readyTimeout bounds the dependency check of one GET /readyz, so a hung
// database makes the probe fail instead of hang.
const readyTimeout = 2 * time.Second

// Pinger checks that a dependency is reachable. *pgxpool.Pool implements it.
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthResponse struct {
	Status string `json:"status"`
}

// healthz handles GET /healthz: the process is up and serving.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// readyz handles GET /readyz: 200 while the database answers a ping, 503
// otherwise (README §4.3).
func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.ready.Ping(ctx); err != nil {
		a.log(r).Warn("readiness check failed", slog.Any("error", err))
		writeError(w, http.StatusServiceUnavailable, codeNotReady, "database unreachable")
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
