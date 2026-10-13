package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"distributed/platform/concurrency"
	"distributed/platform/ctxutil"
)

func main() {
	// Cancelled on Ctrl+C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runWorkerPool(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Fire-and-forget work that must survive the request finishing.
		bg, cancel := ctxutil.Detach(r.Context(), time.Second)
		go func() {
			defer cancel()
			select {
			case <-time.After(100 * time.Millisecond):
				log.Printf("background audit done (ctx err: %v)", bg.Err())
			case <-bg.Done():
				// only if Detach's own 1s timeout passes; the request
				// ending does not cancel bg
			}
		}()
		fmt.Fprintln(w, "hello")
	})

	srv := &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		log.Println("listening on :8080")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("listen: %v", err)
			stop()
		}
	}()

	if err := concurrency.GracefulShutdown(ctx, 30*time.Second, srv); err != nil {
		log.Printf("shutdown: %v", err)
		os.Exit(1)
	}
	log.Println("shutdown complete")
}

func runWorkerPool(ctx context.Context) {
	const workers = 4
	wp := concurrency.NewWorkerPool(ctx, workers)

	for i := 0; i < 20; i++ {
		err := wp.Submit(func() error {
			time.Sleep(50 * time.Millisecond)
			if i%2 == 1 {
				return fmt.Errorf("job %d failed", i)
			}
			return nil
		})
		if err != nil {
			log.Printf("submit: %v", err)
			break
		}
	}

	for _, err := range wp.Close() {
		log.Printf("worker error: %v", err)
	}
}
