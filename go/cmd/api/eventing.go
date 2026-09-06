package main

// The CQRS/streaming seam. Three interfaces from github.com/jrgensen/cqrs are
// all a projection or a command ever sees:
//
//	cqrs.Publisher  command side    — metatagger over JetStream
//	cqrs.Writer     projection side — deadletter wrapping sqlpersister
//	cqrs.Reader     query side      — the *sql.DB itself
//
// Keeping the concrete types in this one file is what lets nathejk/table/... be
// lifted to shared-go later: nothing there imports jetstream, metatagger or a
// driver.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jrgensen/cqrs"
	"github.com/jrgensen/cqrs/deadletter"
	"github.com/jrgensen/cqrs/sqlpersister"
	"github.com/jrgensen/stream"
	"github.com/jrgensen/stream/jetstream"
	"github.com/jrgensen/stream/metatagger"
	"github.com/jrgensen/stream/xstream"
	"github.com/nathejk/shared-go/messages"

	"foto.nathejk.dk/internal/vcs"
)

// producerName is stamped on every event this service publishes.
//
// It must not be a copy of another repo's value: provenance is the only way to
// tell, a year later, which service wrote a message onto a stream several
// services share.
const producerName = "foto-api"

// ErrNoJetstreamDSN means no broker was configured. Like ErrNoDSN this is a
// legitimate mode — projections are built, but nothing feeds them.
var ErrNoJetstreamDSN = errors.New("no JetStream DSN configured")

type eventing struct {
	// mu guards the fields the background connector installs.
	mu        sync.Mutex
	stream    stream.Stream
	publisher cqrs.Publisher

	writer *deadletter.Writer
	reader cqrs.Reader

	// held is the publisher as handlers see it: swapped in when the broker
	// connects, so a handler that starts before the connection does not capture a
	// nil and keep it forever.
	held atomic.Pointer[cqrs.Publisher]

	// mux is a locally declared interface rather than *xstream.Mux so it can be
	// nil (no broker) and faked in a test.
	mux interface {
		AddConsumer(...stream.Consumer)
		Run(ctx context.Context) error
	}
}

// openEventing builds the writer and reader, which need only a database.
//
// The broker is deliberately not required here: projections must be constructible
// — and therefore their schemas creatable — before anything connects to NATS.
func openEventing(cfg config, db *sql.DB) (*eventing, error) {
	if db == nil {
		return nil, ErrNoDSN
	}

	writer := deadletter.New(sqlpersister.New(db), db)
	if err := writer.Consume(writer.CreateTableSql()); err != nil {
		return nil, fmt.Errorf("create deadletter table: %w", err)
	}
	// Projections are rebuilt by replaying the stream on every boot, so
	// dead-letters captured during the previous run's replay are stale.
	if err := writer.Reset(); err != nil {
		return nil, fmt.Errorf("reset deadletter table: %w", err)
	}

	ev := &eventing{writer: writer, reader: db}

	if cfg.jetstreamDSN == "" {
		return ev, ErrNoJetstreamDSN
	}
	return ev, nil
}

func (ev *eventing) connect(cfg config, logger *slog.Logger) error {
	js, err := jetstream.New(cfg.jetstreamDSN)
	if err != nil {
		return fmt.Errorf("connect jetstream: %w", err)
	}

	publisher, err := metatagger.New(js, messages.Metadata{
		Producer: producerName,
		Version:  vcs.Version(),
	})
	if err != nil {
		_ = js.Close()
		return fmt.Errorf("create publisher: %w", err)
	}

	ev.mu.Lock()
	ev.stream = js
	ev.publisher = publisher
	ev.mux = xstream.NewMux(js)
	ev.mu.Unlock()

	// Stored through an explicitly typed interface variable: metatagger.New returns
	// a concrete type, and &publisher would be a pointer-to-concrete, not the
	// pointer-to-interface the atomic holds.
	var held cqrs.Publisher = publisher
	ev.held.Store(&held)

	logger.Info("jetstream connected", "producer", producerName)
	return nil
}

// connectInBackground retries the broker connection with capped backoff, calling
// onConnect once it succeeds.
//
// Background rather than blocking so the container becomes healthy — and answers
// /api/healthcheck — while NATS is still starting. Ingest is refused until this
// completes; see publisherOrNil.
func (ev *eventing) connectInBackground(ctx context.Context, cfg config, logger *slog.Logger, onConnect func()) {
	go func() {
		const (
			initialDelay = time.Second
			maxDelay     = 30 * time.Second
		)
		delay := initialDelay

		for attempt := 1; ; attempt++ {
			if err := ev.connect(cfg, logger); err == nil {
				if onConnect != nil {
					onConnect()
				}
				return
			} else if attempt == 1 {
				logger.Warn("broker unreachable, retrying in the background", "err", err)
			} else {
				logger.Debug("broker still unreachable", "attempt", attempt, "err", err)
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < maxDelay {
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
			}
		}
	}()
}

// registerProjections fans subjects out to the given consumers.
func (ev *eventing) registerProjections(logger *slog.Logger, projections ...cqrs.Consumer) {
	if ev == nil {
		return
	}
	ev.mu.Lock()
	mux := ev.mux
	ev.mu.Unlock()

	if mux == nil {
		if len(projections) > 0 {
			logger.Warn("no broker: projections registered but will not receive events",
				"count", len(projections))
		}
		return
	}

	consumers := make([]stream.Consumer, 0, len(projections))
	subjects := 0
	for _, p := range projections {
		consumers = append(consumers, p)
		subjects += len(p.Consumes())
	}
	mux.AddConsumer(consumers...)

	logger.Info("projections registered", "count", len(projections), "subjects", subjects)
}

// run subscribes the registered consumers. It returns once the subscriptions are
// established — it does not block.
func (ev *eventing) run(ctx context.Context) error {
	if ev == nil {
		return nil
	}
	ev.mu.Lock()
	mux := ev.mux
	ev.mu.Unlock()
	if mux == nil {
		return nil
	}
	return mux.Run(ctx)
}

// publisherOrNil returns the publisher, or nil when the broker is not connected.
//
// Returning an explicit nil rather than a nil-valued interface matters: a typed
// nil inside a non-nil interface compares != nil, and the ingest path checks
// this to decide whether it can honestly accept a photo.
func (ev *eventing) publisherOrNil() cqrs.Publisher {
	if ev == nil {
		return nil
	}
	if p := ev.held.Load(); p != nil {
		return *p
	}
	return nil
}

func (ev *eventing) arm() {
	if ev == nil || ev.writer == nil {
		return
	}
	ev.writer.Arm()
}

func (ev *eventing) deadletterCount() (int, error) {
	if ev == nil || ev.writer == nil {
		return 0, nil
	}
	return ev.writer.Count()
}

// watchDeadletters logs periodically, but only when there is something to say.
func (ev *eventing) watchDeadletters(ctx context.Context, logger *slog.Logger, every time.Duration) {
	if ev == nil || ev.writer == nil || every <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := ev.deadletterCount()
				if err != nil {
					logger.Error("reading dead-letter count", "err", err)
					continue
				}
				if n > 0 {
					logger.Warn("dead-lettered statements waiting", "count", n)
				}
			}
		}
	}()
}

func (ev *eventing) close() error {
	if ev == nil {
		return nil
	}
	ev.mu.Lock()
	s := ev.stream
	ev.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}
