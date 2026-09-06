package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Serve runs an HTTP server on port and shuts it down gracefully on SIGINT or
// SIGTERM.
//
// Graceful rather than abrupt because this service answers a webhook only after
// it has stored bytes and published an event. A request killed mid-flight is a
// photo that kamera believes failed while it may in fact be recorded — so
// in-flight requests are allowed to finish.
func (a *JsonApi) Serve(handler http.Handler, port int) error {
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      handler,
		IdleTimeout:  time.Minute,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	shutdownErr := make(chan error, 1)

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		s := <-quit

		if a.Logger != nil {
			a.Logger.Info("shutting down server", "signal", s.String())
		}

		// Long enough for an in-flight ingest — fetch, store, publish — to finish.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		shutdownErr <- srv.Shutdown(ctx)
	}()

	if a.Logger != nil {
		a.Logger.Info("starting server", "addr", srv.Addr)
	}

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	if err := <-shutdownErr; err != nil {
		return err
	}

	if a.Logger != nil {
		a.Logger.Info("stopped server", "addr", srv.Addr)
	}
	return nil
}
