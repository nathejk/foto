package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// ErrNoDSN means no database was configured. A legitimate mode: the binary still
// starts and serves /api/healthcheck, which is what makes a container come up
// before its database does.
var ErrNoDSN = errors.New("no database DSN configured")

// openDB connects to MariaDB and verifies the connection.
//
// Two DSN parameters are load-bearing and must be present in every environment:
//
//   - parseTime=true, or DATETIME columns will not scan into time.Time.
//   - multiStatements=true, because shared-go's table.sql files may hold more
//     than one statement, and a projection that cannot create its schema is a
//     projection that silently never converges.
func openDB(cfg config, logger *slog.Logger) (*sql.DB, error) {
	if cfg.dbDSN == "" {
		return nil, ErrNoDSN
	}

	db, err := sql.Open("mysql", cfg.dbDSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(cfg.dbMaxOpenConns)
	db.SetMaxIdleConns(cfg.dbMaxIdleConns)
	db.SetConnMaxLifetime(cfg.dbConnMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if logger != nil {
		logger.Info("database connected")
	}
	return db, nil
}
