package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	// Embed the timezone database. The prod image is bare alpine, and CapturedAt
	// timestamps are compared and formatted; a missing tzdata would make that fail
	// only in production.
	_ "time/tzdata"

	"github.com/jrgensen/cqrs"

	"foto.nathejk.dk/cmd/api/app"
	"foto.nathejk.dk/internal/teamnumber"
	"foto.nathejk.dk/internal/vcs"
)

// application is the dependency container handlers hang off. It embeds
// app.JsonApi to inherit WriteJSON/ReadJSON and the standard error responses.
type application struct {
	app.JsonApi

	config   config
	eventing *eventing

	// teams resolves a kamera teamNumber to a domain teamID. Nil when there is
	// no database, in which case ingest must refuse rather than guess.
	teams *teamnumber.Resolver
}

// @title        Nathejk Foto API
// @version      0.1.0
// @description  The entrypoint for photographs into the nathejk ecosystem. Receives kamera's capture callback, stores the original and its renditions, and publishes NATHEJK.<year>.patrulje.<teamID>.photographed.
// @BasePath     /api
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := loadConfig()
	logger.Info("starting foto-api", "version", vcs.Version(), "env", cfg.env, "eventYear", cfg.eventYear)

	// No database and no broker are both legitimate startup modes: the container
	// must come up and answer its healthcheck before its dependencies do. What is
	// not legitimate is silently accepting photos in that state, which is why the
	// ingest path checks for the pieces it needs rather than assuming them.
	db, dbErr := openDB(cfg, logger)
	if dbErr != nil && !errors.Is(dbErr, ErrNoDSN) {
		return dbErr
	}
	if errors.Is(dbErr, ErrNoDSN) {
		logger.Warn("no DB_DSN configured: projections are not maintained and team numbers cannot be resolved")
	}
	if db != nil {
		defer func() { _ = db.Close() }()
	}

	ev, evErr := openEventing(cfg, db)
	if evErr != nil && !errors.Is(evErr, ErrNoJetstreamDSN) && !errors.Is(evErr, ErrNoDSN) {
		return evErr
	}
	if errors.Is(evErr, ErrNoJetstreamDSN) {
		logger.Warn("no JETSTREAM_DSN configured: no events are consumed or published")
	}

	patruljer, teams := newPatruljeProjection(ev, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if ev != nil {
		defer func() {
			if err := ev.close(); err != nil {
				logger.Error("closing event stream", "err", err)
			}
		}()

		if cfg.jetstreamDSN != "" {
			ev.connectInBackground(ctx, cfg, logger, func() {
				var projections []cqrs.Consumer
				if patruljer != nil {
					projections = append(projections, patruljer)
				}
				ev.registerProjections(logger, projections...)

				if err := ev.run(ctx); err != nil {
					logger.Error("starting projections", "err", err)
					return
				}
				// Armed only after the replay: dead-letters during a rebuild are
				// expected noise, dead-letters afterwards are a real disagreement
				// between the log and the read model.
				ev.arm()

				if n, err := ev.deadletterCount(); err != nil {
					logger.Error("reading dead-letter count", "err", err)
				} else if n > 0 {
					logger.Warn("replay produced dead-lettered statements", "count", n)
				} else {
					logger.Info("projections running, dead-letter queue empty")
				}

				ev.watchDeadletters(ctx, logger, 5*time.Minute)
			})
		}
	}

	application := &application{
		JsonApi:  app.JsonApi{Logger: logger},
		config:   cfg,
		eventing: ev,
		teams:    teams,
	}

	return application.Serve(application.routes(), cfg.port)
}

// ready reports whether the service can currently accept a photo.
//
// All three pieces are required and none is optional: without a broker the event
// cannot be published, and without the resolver the teamID cannot be found. This
// is checked before ingest rather than discovered halfway through, because the
// callback is answered only once the whole pipeline has succeeded.
func (app *application) ready() error {
	if app.teams == nil {
		return errors.New("no team resolver: database unavailable")
	}
	if app.eventing == nil || app.eventing.publisherOrNil() == nil {
		return errors.New("no event publisher: broker unavailable")
	}
	return nil
}
