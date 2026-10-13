package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// LivenessHandler answers "is the process alive?". It deliberately checks
// no dependencies: if it fails, the orchestrator restarts the process,
// which does not fix a database outage.
func LivenessHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

// Check is one readiness dependency check.
type Check struct {
	Name string
	Func func(context.Context) error
}

type readiness struct {
	Status  string   `json:"status"`
	Failing []string `json:"failing,omitempty"`
}

// readinessTimeout bounds all checks together, so a hung dependency makes
// the probe fail rather than time out.
const readinessTimeout = 2 * time.Second

// ReadinessHandler answers "can I serve traffic right now?" by running
// every check. On failure it returns 503, which takes the instance out of
// load balancing without restarting it. Failure details are logged, and
// only check names are returned to the caller.
func ReadinessHandler(checks ...Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		// Concurrently, so one hanging dependency cannot use up the
		// budget of the others. All share the same deadline through ctx.
		errs := make([]error, len(checks))
		var wg sync.WaitGroup
		for i, c := range checks {
			wg.Go(func() { errs[i] = c.Func(ctx) })
		}
		wg.Wait()

		var failing []string
		for i, err := range errs {
			if err != nil {
				slog.WarnContext(r.Context(), "readiness check failed", "check", checks[i].Name, "error", err)
				failing = append(failing, checks[i].Name)
			}
		}

		if len(failing) > 0 {
			WriteJSON(w, http.StatusServiceUnavailable, readiness{Status: "not ready", Failing: failing})
			return
		}
		WriteJSON(w, http.StatusOK, readiness{Status: "ready"})
	}
}
