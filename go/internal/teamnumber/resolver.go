// Package teamnumber resolves the patrol number a human typed into the camera
// app onto the teamID the domain uses.
//
// # Why this exists at all
//
// kamera's webhook carries `teamNumber` — "42", what the photo crew typed on a
// phone — but the event this service publishes is subject-addressed by teamID
// (`NATHEJK.<year>.patrulje.<teamID>.photographed`). Those are different
// identifiers: a teamID is `team-<uuid>` and permanent, a teamNumber is a small
// integer allocated per season by patrulje.AssignNumber. Nothing in the webhook
// bridges them, so the bridge is here.
//
// # Why it reads shared-go's projection instead of building its own
//
// `github.com/nathejk/shared-go/tables/patrulje` already projects both `year`
// and `teamNumber` onto the `patrulje` table from
// NATHEJK.*.patrulje.*.{signedup,updated,numberassigned,started}, and its
// table.sql already carries `KEY idx_patrulje_year_number (year, teamNumber)` —
// the exact index this lookup wants. A second projection of the same events
// would be a duplicate that can disagree with the first, and "which of the two
// mappings is right" is not a question worth being able to ask.
//
// # Why the SQL is here and not in shared-go
//
// It should be in shared-go, as `querier.GetByNumber`. It is not, because that
// querier has only GetAll, GetByID and GetLastWithNumber today (and
// GetLastWithNumber finds the highest allocated number for AssignNumber — it is
// not a lookup). Adding it there means the two-repo loop: push shared-go, then
// bump the pin here, or the GOWORK=off build fails. This package is the
// temporary half of that: when GetByNumber lands upstream, Resolver keeps its
// signature and delegates, and nothing else in foto changes.
package teamnumber

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jrgensen/cqrs"
	"github.com/nathejk/shared-go/types"
)

// ErrNotFound means no patrulje in that year holds that number.
//
// Not an error the caller should treat as a failure: the usual cause is a photo
// arriving before the team's events have been projected, or a crew typo. The
// ingest path parks such a photo rather than dropping it (PRD 001 §5).
var ErrNotFound = errors.New("teamnumber: no patrulje with that number")

// ErrAmbiguous means more than one patrulje in that year holds that number.
//
// This is deliberately an error rather than "pick the first". Nothing enforces
// uniqueness upstream — shared-go's idx_patrulje_year_number is a plain KEY, not
// UNIQUE, and AssignNumber allocates from GetLastWithNumber with no constraint
// behind it — so duplicates are possible in principle. Choosing silently would
// attribute a photograph to whichever row happened to sort first, and the
// mistake would be invisible: the wrong team simply has an extra photo. Failing
// loudly puts it in the log while somebody can still fix it. (PRD 001 §11 Q2.)
var ErrAmbiguous = errors.New("teamnumber: more than one patrulje with that number")

// queryTimeout bounds the lookup. It sits on the synchronous callback path — the
// webhook is not answered until the photo is stored and published — so a
// database that has stopped answering must surface as a failed callback that
// kamera will log and can replay, not as a request hanging until kamera's own
// timeout fires.
const queryTimeout = 3 * time.Second

// Resolver looks up teamIDs in the patrulje projection.
//
// It takes a cqrs.Reader rather than a *sql.DB for the same reason shared-go's
// entities do: the read side is an interface so a test can supply anything that
// answers queries, and so nothing here can accidentally write.
type Resolver struct {
	db cqrs.Reader
}

// NewResolver returns a Resolver reading through db.
func NewResolver(db cqrs.Reader) *Resolver {
	return &Resolver{db: db}
}

// TeamIDByNumber returns the teamID of the patrulje holding number in year.
//
// year is the *season*, not the calendar year of the photograph. The distinction
// is real and load-bearing: shared-go's projector takes `year` from the event
// subject — the season the team signed up for — and its own comment notes that
// this differs from the calendar year once a season opens in the preceding one.
// kamera, meanwhile, derives its paths from DateTime.UtcNow. So the caller must
// pass the configured EVENT_YEAR and must not pass the photo's timestamp year.
func (r *Resolver) TeamIDByNumber(ctx context.Context, year, number string) (types.TeamID, error) {
	if r == nil || r.db == nil {
		return "", errors.New("teamnumber: no reader configured")
	}

	year = strings.TrimSpace(year)
	number = strings.TrimSpace(number)

	// Both empty checks matter, and for different reasons. An empty year would
	// match rows from every season; an empty number would match every patrulje
	// that has not been assigned one yet, since the column defaults to "" rather
	// than NULL. Either would return a real teamID for a meaningless question.
	if year == "" {
		return "", errors.New("teamnumber: year is empty")
	}
	if number == "" {
		return "", fmt.Errorf("teamnumber: %w", ErrNotFound)
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// LIMIT 2, not LIMIT 1: one extra row is what makes ErrAmbiguous detectable
	// at all. With LIMIT 1 a duplicate is indistinguishable from a unique match.
	//
	// teamNumber is compared as a string because that is how it is stored (see
	// shared-go's Patrulje.TeamNumber and the length()-based sort in
	// AssignNumber). No numeric coercion: CAST would make "042" and "42" equal
	// here while they stay distinct in the column, so a lookup could succeed
	// against a row no other query in the system considers a match.
	const query = `SELECT teamId FROM patrulje WHERE year = ? AND teamNumber = ? LIMIT 2`

	rows, err := r.db.QueryContext(ctx, query, year, number)
	if err != nil {
		return "", fmt.Errorf("teamnumber: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var found []types.TeamID
	for rows.Next() {
		var id types.TeamID
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("teamnumber: scan: %w", err)
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("teamnumber: rows: %w", err)
	}

	switch len(found) {
	case 0:
		return "", fmt.Errorf("teamnumber: year %q number %q: %w", year, number, ErrNotFound)
	case 1:
		// A row can exist with an empty teamId only if the projection wrote one,
		// which would be a bug upstream — but an empty teamID would go on to
		// build the subject `NATHEJK.<year>.patrulje..photographed`, whose empty
		// token makes the per-team purge pattern stop matching. Cheaper to catch
		// here than to have an unerasable event.
		if strings.TrimSpace(string(found[0])) == "" {
			return "", fmt.Errorf("teamnumber: year %q number %q: %w", year, number, ErrNotFound)
		}
		return found[0], nil
	default:
		return "", fmt.Errorf("teamnumber: year %q number %q: %w", year, number, ErrAmbiguous)
	}
}
