package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	// Embed the timezone database. The prod image is bare alpine, and CapturedAt
	// timestamps are compared and formatted; a missing tzdata would make that fail
	// only in production.
	_ "time/tzdata"

	"github.com/jrgensen/cqrs"

	bff "foto.nathejk.dk/cmd/api/app"
	"foto.nathejk.dk/internal/blob"
	"foto.nathejk.dk/internal/fetcher"
	"foto.nathejk.dk/internal/teamnumber"
	"foto.nathejk.dk/internal/vcs"
	"foto.nathejk.dk/nathejk/table/photo"
)

// application is the dependency container handlers hang off. It embeds bff.JsonApi
// to inherit WriteJSON/ReadJSON and the standard error responses.
//
// The transport package is imported as `bff` because every method here has a
// receiver named `app`, which would otherwise shadow it.
type application struct {
	bff.JsonApi

	config   config
	eventing *eventing

	// teams resolves a camera-app teamNumber to a domain teamID. Nil when there is
	// no database, in which case ingest must refuse rather than guess.
	teams *teamnumber.Resolver

	// photoTable is the photograph entity: it publishes the event, projects it and
	// answers the reads. Nil without a database.
	photoTable *photo.Table

	// blobs holds the bytes — the only state that cannot be rebuilt from the stream.
	blobs blob.Store

	// photos fetches an image from the URL the webhook carries. Named for what it
	// gets rather than how: it is a deliberately paranoid HTTP client.
	photos *fetcher.Fetcher
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

	// The photograph entity. Constructed with the publisher accessor rather than a
	// captured publisher, so it still works when the broker connects later.
	var photos *photo.Table
	if ev != nil && ev.writer != nil {
		p, err := photo.New(publisherFunc(ev), ev.writer, ev.reader)
		if err != nil {
			return err
		}
		photos = p
	}

	blobs, err := openBlobStore(cfg, logger)
	if err != nil {
		return err
	}

	photoFetcher, err := fetcher.New(cfg.photoHosts, cfg.maxPhotoBytes, cfg.fetchTimeout)
	if err != nil {
		return fmt.Errorf("configure photo fetcher: %w", err)
	}
	if len(cfg.photoHosts) == 0 {
		// Fails closed, so this is a warning rather than a silent hole — but it means
		// every ingest will be refused, which is worth saying loudly at boot rather
		// than discovering per photograph.
		logger.Warn("PHOTO_HOSTS is empty: every imageUrl will be refused")
	}
	if cfg.webhookSecret == "" {
		logger.Warn("WEBHOOK_SECRET is empty: the camera app's callback is unauthenticated")
	}

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
				if photos != nil {
					projections = append(projections, photos)
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
		JsonApi:    bff.JsonApi{Logger: logger},
		config:     cfg,
		eventing:   ev,
		teams:      teams,
		photoTable: photos,
		blobs:      blobs,
		photos:     photoFetcher,
	}

	return application.Serve(application.routes(), cfg.port)
}

// openBlobStore returns the object store, falling back to memory when no path is
// configured.
//
// The fallback is loud, because an in-memory store loses every photograph on
// restart. It exists so the binary is runnable in a test or a bare container, not as
// a mode anything should run in.
func openBlobStore(cfg config, logger *slog.Logger) (blob.Store, error) {
	if cfg.blobPath == "" {
		logger.Warn("no BLOB_PATH configured: photos are kept in memory and will not survive a restart")
		return blob.NewMemoryStore(), nil
	}
	store, err := blob.NewFileStore(cfg.blobPath)
	if err != nil {
		// Not a fallback to memory. If the directory cannot be made private, the right
		// outcome is to refuse to start rather than to store photographs of children
		// somewhere world-readable — and refusing to start is visible, whereas a
		// warning at boot is not.
		return nil, fmt.Errorf("blob store at %s: %w", cfg.blobPath, err)
	}
	logger.Info("blob store ready", "path", cfg.blobPath)
	return store, nil
}

// publisherFunc adapts the eventing seam to what an entity needs.
//
// The entity is built before the broker connects, so it cannot be handed a
// publisher: it would capture nil and keep it. This indirection means every publish
// asks for the current one, and gets ErrNoPublisher until there is one.
func publisherFunc(ev *eventing) cqrs.Publisher {
	return lazyPublisher{ev: ev}
}

// lazyPublisher looks the real publisher up per call.
type lazyPublisher struct{ ev *eventing }

func (l lazyPublisher) Publish(msg cqrs.Message) error {
	p := l.ev.publisherOrNil()
	if p == nil {
		return photo.ErrNoPublisher
	}
	return p.Publish(msg)
}

func (l lazyPublisher) MessageFunc() cqrs.MessageFunc {
	if p := l.ev.publisherOrNil(); p != nil {
		return p.MessageFunc()
	}
	// A builder that returns nil rather than a nil builder, which would panic when
	// called. The entity checks for a nil message and reports ErrNoPublisher, so the
	// outage is reported once, in one place, as an error rather than a stack trace.
	return func(cqrs.Subject) cqrs.MutableMessage { return nil }
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
	if app.photoTable == nil {
		return errors.New("no photo read model: database unavailable")
	}
	if app.blobs == nil {
		return errors.New("no blob store")
	}
	if app.eventing == nil || app.eventing.publisherOrNil() == nil {
		return errors.New("no event publisher: broker unavailable")
	}
	return nil
}
