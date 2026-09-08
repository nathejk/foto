package teamnumber

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// newResolver wires a Resolver over a mocked database.
//
// sqlmock rather than a real MariaDB: what is worth testing here is the
// resolution *rules* — not found, ambiguous, empty inputs — and those are
// decisions this package makes about rows, not things MySQL decides. A container
// would make the same assertions slower and less precise.
func newResolver(t *testing.T) (*Resolver, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
		_ = db.Close()
	})
	return NewResolver(db), mock
}

const query = "SELECT teamId FROM patrulje WHERE year = \\? AND teamNumber = \\? LIMIT 2"

func TestTeamIDByNumberResolves(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "42").
		WillReturnRows(sqlmock.NewRows([]string{"teamId"}).AddRow("team-abc"))

	got, err := r.TeamIDByNumber(context.Background(), "2026", "42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "team-abc" {
		t.Errorf("got teamID %q, want %q", got, "team-abc")
	}
}

// Whitespace is trimmed because the number arrives from a query parameter typed
// on a phone. Nothing else is normalised — notably not leading zeros; see
// TestTeamIDByNumberDoesNotNormalizeLeadingZeros.
func TestTeamIDByNumberTrimsWhitespace(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "42").
		WillReturnRows(sqlmock.NewRows([]string{"teamId"}).AddRow("team-abc"))

	if _, err := r.TeamIDByNumber(context.Background(), "  2026 ", " 42\n"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// "042" is not silently treated as "42". The column stores the string the
// allocator wrote, and AssignNumber sorts it by length — so coercing here would
// make this lookup agree with no other query in the system.
func TestTeamIDByNumberDoesNotNormalizeLeadingZeros(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "042").
		WillReturnRows(sqlmock.NewRows([]string{"teamId"}))

	_, err := r.TeamIDByNumber(context.Background(), "2026", "042")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestTeamIDByNumberNotFound(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "999").
		WillReturnRows(sqlmock.NewRows([]string{"teamId"}))

	_, err := r.TeamIDByNumber(context.Background(), "2026", "999")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// Two rows must fail rather than pick one. Nothing upstream enforces uniqueness
// on (year, teamNumber), and attributing a child's photograph to whichever row
// sorted first is a mistake nobody would ever see.
func TestTeamIDByNumberAmbiguous(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "42").
		WillReturnRows(sqlmock.NewRows([]string{"teamId"}).
			AddRow("team-abc").
			AddRow("team-def"))

	_, err := r.TeamIDByNumber(context.Background(), "2026", "42")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("got %v, want ErrAmbiguous", err)
	}
}

// An empty number must not reach the database: teamNumber defaults to "" for
// every patrulje that has not been assigned one, so the query would match a real
// row and return a real teamID for a meaningless question.
func TestTeamIDByNumberEmptyNumberDoesNotQuery(t *testing.T) {
	r, _ := newResolver(t)

	_, err := r.TeamIDByNumber(context.Background(), "2026", "   ")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// An empty year would match rows from every season.
func TestTeamIDByNumberEmptyYearDoesNotQuery(t *testing.T) {
	r, _ := newResolver(t)

	_, err := r.TeamIDByNumber(context.Background(), "", "42")
	if err == nil {
		t.Fatal("expected an error for an empty year")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("an empty year is a caller bug, not a missing team")
	}
}

// A row whose teamId is empty is treated as absent. An empty subject token makes
// the per-team purge pattern stop matching, which would make that event
// unerasable.
func TestTeamIDByNumberRejectsEmptyTeamID(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "42").
		WillReturnRows(sqlmock.NewRows([]string{"teamId"}).AddRow(""))

	_, err := r.TeamIDByNumber(context.Background(), "2026", "42")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestTeamIDByNumberQueryError(t *testing.T) {
	r, mock := newResolver(t)

	mock.ExpectQuery(query).
		WithArgs("2026", "42").
		WillReturnError(errors.New("boom"))

	if _, err := r.TeamIDByNumber(context.Background(), "2026", "42"); err == nil {
		t.Fatal("expected the query error to surface")
	}
}

func TestTeamIDByNumberNoReader(t *testing.T) {
	var r *Resolver
	if _, err := r.TeamIDByNumber(context.Background(), "2026", "42"); err == nil {
		t.Fatal("expected an error from a nil resolver")
	}
	if _, err := NewResolver(nil).TeamIDByNumber(context.Background(), "2026", "42"); err == nil {
		t.Fatal("expected an error from a resolver with no reader")
	}
}
