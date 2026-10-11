package concurrency

import (
	"context"
	"errors"
	"time"
)

// Shutdowner is implemented by anything that can be stopped gracefully
// within a deadline, e.g. *http.Server.
type Shutdowner interface {
	Shutdown(context.Context) error
}

// GracefulShutdown blocks until ctx is done, then shuts down servers in
// order, sharing a single timeout. All errors are joined.
func GracefulShutdown(ctx context.Context,
	timeout time.Duration,
	servers ...Shutdowner) error {
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var errs []error
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
