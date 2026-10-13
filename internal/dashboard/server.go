package dashboard

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"distributed/platform/middleware"
)

//go:embed static
var static embed.FS

type Config struct {
	Port int
}

func NewServer(cfg Config, c *Collector) *http.Server {
	page, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // the embedded directory is fixed at compile time
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(page))
	mux.HandleFunc("GET /api/status", statusHandler(c))
	mux.HandleFunc("GET /healthz/live", middleware.LivenessHandler)

	// No Logging middleware: the page polls every 2 seconds, which would
	// drown the log.
	handler := middleware.Chain(
		mux,
		middleware.RequestID(),
		middleware.Recover(),
		middleware.Timeout(10*time.Second),
	)

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func statusHandler(c *Collector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		w.Header().Set("Cache-Control", "no-store")
		middleware.WriteJSON(w, http.StatusOK, c.Collect(ctx))
	}
}
