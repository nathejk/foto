package main

// Configuration is read from environment variables here, with sensible dev
// defaults, and passed down through the config struct. Never read os.Getenv
// deeper in the call tree — a value that appears halfway down is a value nobody
// can find when it is wrong.

import (
	"flag"
	"os"
	"strconv"
	"time"
)

type config struct {
	port int
	env  string

	dbDSN             string
	dbMaxOpenConns    int
	dbMaxIdleConns    int
	dbConnMaxLifetime time.Duration

	jetstreamDSN string

	// eventYear is the *season*, and is the year used to resolve a team number
	// and to build event subjects.
	//
	// It cannot be derived from the clock. shared-go's patrulje projector takes
	// `year` from the event subject — the season a team signed up for — and its
	// own comment records that this stops matching the calendar year once a
	// season opens in the preceding one. kamera, by contrast, derives its paths
	// from DateTime.UtcNow. Reading the calendar here would therefore silently
	// fail to resolve any team during exactly that window.
	eventYear string

	// blobPath is where photo objects live. It is the only state this service
	// cannot rebuild by replaying the stream, and therefore the only thing that
	// must be backed up.
	blobPath string

	// webhookSecret is the shared secret kamera sends as X-Webhook-Secret.
	// Empty disables the check, which is only ever right in dev.
	webhookSecret string
}

func loadConfig() config {
	var cfg config

	flag.IntVar(&cfg.port, "port", envInt("PORT", 4000), "API server port")
	flag.StringVar(&cfg.env, "env", envStr("ENV", "development"), "Environment (development|staging|production)")

	flag.StringVar(&cfg.dbDSN, "db-dsn", envStr("DB_DSN", ""), "MariaDB DSN (empty runs without a database)")
	flag.IntVar(&cfg.dbMaxOpenConns, "db-max-open-conns", envInt("DB_MAX_OPEN_CONNS", 25), "Maximum open database connections")
	flag.IntVar(&cfg.dbMaxIdleConns, "db-max-idle-conns", envInt("DB_MAX_IDLE_CONNS", 25), "Maximum idle database connections")
	flag.DurationVar(&cfg.dbConnMaxLifetime, "db-conn-max-lifetime", envDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute), "Maximum lifetime of a database connection")

	flag.StringVar(&cfg.jetstreamDSN, "jetstream-dsn", envStr("JETSTREAM_DSN", ""), "NATS JetStream DSN (empty runs without a broker)")

	flag.StringVar(&cfg.eventYear, "event-year", envStr("EVENT_YEAR", currentYear()), "Season year used to resolve team numbers and build subjects")
	flag.StringVar(&cfg.blobPath, "blob-path", envStr("BLOB_PATH", ""), "Directory for photo objects (empty keeps them in memory)")
	flag.StringVar(&cfg.webhookSecret, "webhook-secret", envStr("WEBHOOK_SECRET", ""), "Shared secret kamera sends as X-Webhook-Secret (empty disables the check)")

	flag.Parse()
	return cfg
}

// currentYear is the fallback for EVENT_YEAR. It is a fallback, not a default
// worth relying on — see config.eventYear.
func currentYear() string {
	return strconv.Itoa(time.Now().UTC().Year())
}

func envStr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
