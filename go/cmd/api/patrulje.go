package main

// The patrulje projection.
//
// foto does not own patruljer and never writes one. It mounts shared-go's
// entity read-only, purely so that the number a photo crew typed into kamera can
// be turned into the teamID the event subject needs:
//
//	kamera webhook  →  teamNumber "42"
//	patrulje table  →  teamId "team-<uuid>"
//	subject         →  NATHEJK.<year>.patrulje.<teamId>.photographed
//
// # Why mount the shared entity rather than project the events here
//
// shared-go's projector already derives `year` and `teamNumber` from
// NATHEJK.*.patrulje.*.{signedup,updated,numberassigned,started}, and its
// table.sql already indexes (year, teamNumber). Re-deriving the same mapping from
// the same events would produce a second answer that can disagree with the first,
// and "which mapping is correct" is not a question worth being able to ask.
//
// # What mounting a shared entity costs
//
// foto is the first Nathejk service to do this — hej imports only shared-go's
// `messages` and `types` and defines its own tables — so the sharp edges are
// worth naming:
//
//  1. patrulje.New returns an *unexported* type, so it cannot be named in a field
//     or a signature here. It is held behind patruljeProjection below, which is
//     the right coupling anyway: foto wants a consumer, not an entity.
//  2. patrulje.New calls log.Fatalf if it cannot create its schema, instead of
//     returning an error. That cannot be softened from here; it means a database
//     that rejects the schema is a hard stop at boot rather than a degraded start.
//     Deliberately not worked around — a foto with no team mapping could not
//     resolve a single photo.
//  3. Its querier has no lookup by team number. See internal/teamnumber.

import (
	"log/slog"

	"github.com/jrgensen/cqrs"
	"github.com/nathejk/shared-go/tables/patrulje"

	"foto.nathejk.dk/internal/teamnumber"
)

// patruljeProjection is the slice of shared-go's patrulje entity foto uses: a
// consumer to register on the mux, and nothing else.
//
// Declared here rather than imported because patrulje.New's return type is
// unexported. Narrow on purpose — foto must not grow the ability to publish a
// patrulje command by accident, and this interface is what prevents it.
type patruljeProjection interface {
	cqrs.Consumer
}

// newPatruljeProjection mounts the projection and returns it together with a
// resolver reading the table it maintains.
//
// The publisher is passed as nil: foto issues no patrulje commands. The entity's
// commander tolerates it because nothing here ever calls one — and if that
// changes, a nil publisher panicking at the call site is a better outcome than
// this service quietly acquiring the authority to renumber a team.
func newPatruljeProjection(ev *eventing, logger *slog.Logger) (patruljeProjection, *teamnumber.Resolver) {
	if ev == nil || ev.writer == nil || ev.reader == nil {
		// No database. Nothing to project into and nothing to read, so the caller
		// gets nils and the ingest path refuses photos rather than resolving them
		// against an absent table.
		logger.Warn("no database: patrulje projection not mounted, team numbers cannot be resolved")
		return nil, nil
	}

	// Constructing this creates the `patrulje` table if it does not exist.
	table := patrulje.New(nil, ev.writer, ev.reader)

	logger.Info("patrulje projection mounted", "subjects", len(table.Consumes()))

	return table, teamnumber.NewResolver(ev.reader)
}
