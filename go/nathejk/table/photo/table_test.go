package photo

import (
	"strings"
	"testing"
	"time"

	"github.com/jrgensen/cqrs"
	"github.com/jrgensen/cqrs/cqrstest"
)

func ref(c byte) string { return strings.Repeat(string(c), 64) }

func newTable(t *testing.T) (*Table, *cqrstest.Writer, *cqrstest.Publisher) {
	t.Helper()
	w := &cqrstest.Writer{}
	p := &cqrstest.Publisher{}
	table, err := New(p, w, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The schema statement is not interesting to the assertions that follow.
	w.Reset()
	return table, w, p
}

func message(t *testing.T, subj string, body any) cqrs.Message {
	t.Helper()
	msg := cqrstest.NewMessage(cqrs.SubjectFromStr(subj))
	if err := msg.SetBody(body); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	return msg
}

func TestNewCreatesSchema(t *testing.T) {
	w := &cqrstest.Writer{}
	if _, err := New(nil, w, nil); err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(w.Statements) != 1 || !strings.Contains(w.Statements[0], "CREATE TABLE IF NOT EXISTS photo") {
		t.Fatalf("expected the schema to be created, got %v", w.Statements)
	}
	// The primary key is what allows several photographs per team. If this
	// assertion ever fails, a second photograph silently overwrites the first.
	if !strings.Contains(w.Statements[0], "PRIMARY KEY (year, teamId, type, ref)") {
		t.Error("the primary key must include ref, or a team can only have one photo")
	}
}

func TestNewRequiresWriter(t *testing.T) {
	if _, err := New(nil, nil, nil); err == nil {
		t.Fatal("expected an error without a Writer")
	}
}

func TestSubjects(t *testing.T) {
	subj, err := PhotographedSubject("2026", "team-abc")
	if err != nil {
		t.Fatalf("PhotographedSubject: %v", err)
	}
	if got, want := subj.Subject(), "NATHEJK.2026.patrulje.team-abc.photographed"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	purge, err := PurgeSubject("2026", "team-abc")
	if err != nil {
		t.Fatalf("PurgeSubject: %v", err)
	}
	if got, want := purge.Subject(), "NATHEJK.2026.patrulje.team-abc.photopurged"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A subject built by this package must be matched by the pattern the projection
// subscribes with. These are two independent strings that have to agree, and
// nothing at runtime would report it if they stopped: the projection would simply
// never see the event.
func TestSubjectsMatchConsumePatterns(t *testing.T) {
	table, _, _ := newTable(t)

	subjects := table.Consumes()
	if len(subjects) != 2 {
		t.Fatalf("expected 2 consumed subjects, got %d", len(subjects))
	}
	// The ":" form must normalise to the dotted form.
	if got, want := subjects[0].Subject(), "NATHEJK.*.patrulje.*.photographed"; got != want {
		t.Errorf("consume pattern: got %q, want %q", got, want)
	}

	photographed, _ := PhotographedSubject("2026", "team-abc")
	if !photographed.Match(matchPattern(photographedVerb)) {
		t.Error("a published photographed subject does not match the pattern the projection consumes")
	}
	purged, _ := PurgeSubject("2026", "team-abc")
	if !purged.Match(matchPattern(purgedVerb)) {
		t.Error("a published purge subject does not match the pattern the projection consumes")
	}
	// And they must not match each other, or one handler would see both.
	if photographed.Match(matchPattern(purgedVerb)) {
		t.Error("photographed must not match the purge pattern")
	}
}

// A dot in a token would split into extra subject tokens: the event would still
// publish and still match NATHEJK.>, while no longer matching the per-team purge
// pattern. That team's photographs would be the ones that could not be erased.
func TestSubjectRejectsBadTokens(t *testing.T) {
	for _, tc := range []struct{ name, year, teamID string }{
		{"empty year", "", "team-abc"},
		{"empty team", "2026", ""},
		{"dot in team", "2026", "team.abc"},
		{"wildcard in team", "2026", "team*"},
		{"deep wildcard in team", "2026", "team>"},
		{"space in team", "2026", "team abc"},
		{"newline in year", "2026\n", "team-abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PhotographedSubject(tc.year, tc.teamID); err == nil {
				t.Errorf("expected %q/%q to be rejected", tc.year, tc.teamID)
			}
			if _, err := PurgeSubject(tc.year, tc.teamID); err == nil {
				t.Errorf("purge: expected %q/%q to be rejected", tc.year, tc.teamID)
			}
		})
	}
}

func TestHandlePhotographedWritesRow(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{
		TeamID: "team-abc", Year: "2026", TeamNumber: "42", Type: "start",
		Ref: ref('a'), ContentType: "image/jpeg", Bytes: 1000, Width: 1024, Height: 768,
		Renditions: []PhotoRendition{
			{Name: "thumb1024", Ref: ref('b'), ContentType: "image/jpeg", Bytes: 500, Width: 1024, Height: 768},
			{Name: "thumb256", Ref: ref('c'), ContentType: "image/jpeg", Bytes: 100, Width: 256, Height: 192},
		},
		Original:   &PhotoOriginal{Ref: ref('d'), ContentType: "image/jpeg", Bytes: 9000, Width: 4000, Height: 3000, Orientation: 6},
		Source:     &PhotoSource{URL: "https://foto.nathejk.dk/photos/2026/start/Team-42_1.jpg", Kind: "kamera-webhook"},
		CapturedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
	}

	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	stmt := w.Last()
	for _, want := range []string{
		"INSERT INTO photo SET",
		"ON DUPLICATE KEY UPDATE",
		"year='2026'", "teamId='team-abc'", "teamNumber='42'", "type='start'",
		"ref='" + ref('a') + "'",
		"originalRef='" + ref('d') + "'",
		"orientation=6",
		"capturedAt='2026-09-07 12:00:00'",
		// The smallest rendition is denormalized, not the first one listed.
		"thumbRef='" + ref('c') + "'",
	} {
		if !strings.Contains(stmt, want) {
			t.Errorf("statement missing %q\ngot: %s", want, stmt)
		}
	}
}

// Two different photographs of one team must produce two rows, which is what the
// content hash in the primary key buys. This is the requirement that separates this
// event from hej's portrait.
func TestSeveralPhotosPerTeamAreDistinctRows(t *testing.T) {
	table, w, _ := newTable(t)

	base := PatruljePhotographed{TeamID: "team-abc", Year: "2026", Type: "start", ContentType: "image/jpeg"}
	first, second := base, base
	first.Ref, second.Ref = ref('a'), ref('b')

	for _, body := range []PatruljePhotographed{first, second} {
		if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)); err != nil {
			t.Fatalf("HandleMessage: %v", err)
		}
	}

	if len(w.Statements) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(w.Statements))
	}
	if w.Statements[0] == w.Statements[1] {
		t.Error("two different photographs produced identical statements")
	}
}

// Re-delivery of the same photograph must converge, not duplicate. This is what
// makes replaying the camera app's failed-webhook log safe.
func TestHandlePhotographedIsIdempotent(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{
		TeamID: "team-abc", Year: "2026", Type: "start", Ref: ref('a'),
		ContentType: "image/jpeg", CapturedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
	}
	msg := message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)

	if err := table.HandleMessage(msg); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := table.HandleMessage(msg); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(w.Statements) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(w.Statements))
	}
	if w.Statements[0] != w.Statements[1] {
		t.Errorf("the same event produced different statements:\n%s\n%s", w.Statements[0], w.Statements[1])
	}
}

// The subject is the fallback for identifiers the body omits, and is the more
// trustworthy source: the broker matched on it.
func TestHandlePhotographedFallsBackToSubject(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{Ref: ref('a'), ContentType: "image/jpeg"}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-xyz.photographed", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	stmt := w.Last()
	if !strings.Contains(stmt, "year='2026'") || !strings.Contains(stmt, "teamId='team-xyz'") {
		t.Errorf("expected year and teamId from the subject, got: %s", stmt)
	}
}

// A ref that is not a content hash must fail the event rather than be written. A
// bad ref in the row would make every later read degrade to "no photo" while the
// row insisted there was one.
func TestHandlePhotographedRejectsBadRef(t *testing.T) {
	for _, bad := range []string{
		"", "not-a-hash", strings.Repeat("g", 64), strings.Repeat("A", 64),
		"../../etc/passwd", strings.Repeat("a", 63), strings.Repeat("a", 65),
	} {
		table, w, _ := newTable(t)
		body := PatruljePhotographed{TeamID: "team-abc", Year: "2026", Ref: bad}
		err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body))
		if err == nil {
			t.Errorf("ref %q: expected an error", bad)
		}
		if len(w.Statements) != 0 {
			t.Errorf("ref %q: nothing should have been written, got %v", bad, w.Statements)
		}
	}
}

// A malformed rendition costs that size, not the photograph: readers fall back to
// the display image, whereas failing would lose the photo over a secondary artefact.
func TestBadRenditionDoesNotFailThePhoto(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{
		TeamID: "team-abc", Year: "2026", Ref: ref('a'), ContentType: "image/jpeg",
		Renditions: []PhotoRendition{
			{Name: "thumb256", Ref: "bogus", Width: 256, Height: 192},
			{Name: "thumb1024", Ref: ref('b'), Width: 1024, Height: 768},
		},
	}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	stmt := w.Last()
	if strings.Contains(stmt, "bogus") {
		t.Error("the malformed rendition should have been dropped")
	}
	if !strings.Contains(stmt, ref('b')) {
		t.Error("the valid rendition should have been kept")
	}
}

// An unnamed rendition is still worth keeping, because a purge has to know its ref
// exists. It gets named by its own size so it is addressable.
func TestUnnamedRenditionGetsASizeName(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{
		TeamID: "team-abc", Year: "2026", Ref: ref('a'),
		Renditions: []PhotoRendition{{Ref: ref('b'), Width: 256, Height: 192}},
	}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !strings.Contains(w.Last(), `thumb256`) {
		t.Errorf("expected a derived name, got: %s", w.Last())
	}
}

// A malformed original ref costs the original, not the photograph.
func TestBadOriginalRefDoesNotFailThePhoto(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{
		TeamID: "team-abc", Year: "2026", Ref: ref('a'),
		Original: &PhotoOriginal{Ref: "bogus", Orientation: 6},
	}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	stmt := w.Last()
	if strings.Contains(stmt, "bogus") {
		t.Error("the malformed original ref should have been dropped")
	}
	if !strings.Contains(stmt, "originalRef=''") {
		t.Errorf("expected an empty original ref, got: %s", stmt)
	}
}

func TestHandlePurgedDeletesNamedRefs(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotoPurged{
		TeamID: "team-abc", Year: "2026",
		Refs: []string{ref('a'), "bogus", ref('b')}, Reason: "retention",
	}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photopurged", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	stmt := w.Last()
	if !strings.HasPrefix(stmt, "DELETE FROM photo WHERE") {
		t.Fatalf("expected a delete, got: %s", stmt)
	}
	for _, want := range []string{ref('a'), ref('b'), "teamId='team-abc'", "year='2026'"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("delete missing %q\ngot: %s", want, stmt)
		}
	}
	if strings.Contains(stmt, "bogus") {
		t.Error("a malformed ref should not reach the statement")
	}
}

// The worst outcome available here would be a purge that widened to the whole team
// because its ref list failed to decode. The empty case must do nothing at all.
func TestHandlePurgedWithNoRefsDoesNothing(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotoPurged{TeamID: "team-abc", Year: "2026"}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photopurged", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if len(w.Statements) != 0 {
		t.Fatalf("expected no statement, got %v", w.Statements)
	}
}

// A subject this projection does not handle must not be an error: the mux may hand
// over anything, and failing would dead-letter somebody else's event.
func TestUnhandledSubjectIsIgnored(t *testing.T) {
	table, w, _ := newTable(t)

	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.started", struct{}{})); err != nil {
		t.Fatalf("expected an unhandled subject to be ignored, got: %v", err)
	}
	if len(w.Statements) != 0 {
		t.Errorf("expected no statement, got %v", w.Statements)
	}
}

func TestPublishPhotographed(t *testing.T) {
	table, _, p := newTable(t)

	err := table.Publish(Photographed{
		Year: "2026", TeamID: "team-abc", TeamNumber: "42", Type: "start",
		Display:    PhotoRendition{Ref: ref('a'), ContentType: "image/jpeg", Bytes: 1000, Width: 1024, Height: 768},
		CapturedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	subjects := p.Subjects()
	if len(subjects) != 1 || subjects[0] != "NATHEJK.2026.patrulje.team-abc.photographed" {
		t.Fatalf("unexpected subjects: %v", subjects)
	}

	// The published event must be one the projection accepts. A publisher and a
	// handler that disagree is the failure this round-trip exists to catch.
	var got PatruljePhotographed
	if err := p.Messages[0].Body(&got); err != nil {
		t.Fatalf("Body: %v", err)
	}
	if got.Ref != ref('a') || got.Width != 1024 {
		t.Errorf("unexpected body: %+v", got)
	}
	if err := table.HandleMessage(p.Messages[0]); err != nil {
		t.Errorf("the projection rejected an event this package published: %v", err)
	}
}

func TestPublishRejectsBadInput(t *testing.T) {
	table, _, _ := newTable(t)

	if err := table.Publish(Photographed{Year: "2026", TeamID: "team-abc"}); err == nil {
		t.Error("expected an error for a missing display ref")
	}
	if err := table.Publish(Photographed{Year: "2026", TeamID: "team.abc",
		Display: PhotoRendition{Ref: ref('a')}}); err == nil {
		t.Error("expected an error for a teamId that is not a subject token")
	}
}

func TestPublishWithoutPublisher(t *testing.T) {
	w := &cqrstest.Writer{}
	table, err := New(nil, w, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = table.Publish(Photographed{Year: "2026", TeamID: "team-abc",
		Display: PhotoRendition{Ref: ref('a')}})
	if err != ErrNoPublisher {
		t.Errorf("got %v, want ErrNoPublisher", err)
	}
}

// An empty purge must be refused at the command side too: a future reader of the
// log could reasonably read "no refs" as "everything".
func TestPurgeRequiresRefs(t *testing.T) {
	table, _, _ := newTable(t)
	if err := table.Purge("2026", "team-abc", "retention", nil); err == nil {
		t.Error("expected an error for a purge with no refs")
	}
}

func TestQuoteEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`plain`, `'plain'`},
		{`it's`, `'it\'s'`},
		{`say "hi"`, `'say \"hi\"'`},
		{`back\slash`, `'back\\slash'`},
		{"new\nline", `'new\nline'`},
		{"ret\rurn", `'ret\rurn'`},
		{"nul\x00byte", `'nul\0byte'`},
		{"ctrl\x1aZ", `'ctrl\ZZ'`},
	} {
		if got := quote(tc.in); got != tc.want {
			t.Errorf("quote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// A trailing backslash is the case Go's %q gets away with and SQL does not: it
// would escape the closing quote and swallow the rest of the statement.
func TestQuoteHandlesTrailingBackslash(t *testing.T) {
	got := quote(`ends with\`)
	if got != `'ends with\\'` {
		t.Errorf("got %s", got)
	}
}

// The injection surface is Type, TeamNumber and the source URL: unlike a ref, none
// of them is validated as hex.
func TestHandlePhotographedEscapesUntrustedFields(t *testing.T) {
	table, w, _ := newTable(t)

	body := PatruljePhotographed{
		TeamID: "team-abc", Year: "2026", Ref: ref('a'),
		Type:       `start'; DROP TABLE photo; --`,
		TeamNumber: `42' OR '1'='1`,
		Source:     &PhotoSource{URL: `https://x/'; DELETE FROM photo WHERE '1'='1`},
	}
	if err := table.HandleMessage(message(t, "NATHEJK.2026.patrulje.team-abc.photographed", body)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	stmt := w.Last()
	// Checking for the payload as a substring would be meaningless — an escaped
	// `\'; DROP` contains `'; DROP`. What matters is that no quote in the statement
	// terminates a literal early, so the literals are scanned properly.
	if !quotesBalanced(stmt) {
		t.Errorf("statement has an unescaped quote and is injectable: %s", stmt)
	}
	if !strings.Contains(stmt, `\'; DROP TABLE`) {
		t.Errorf("expected the payload to be escaped rather than removed: %s", stmt)
	}
}

// quotesBalanced reports whether every single quote in stmt either delimits a
// literal or is escaped inside one.
//
// This is the property that makes the generated statements safe, so it is asserted
// directly rather than by grepping for attack strings — a grep only ever catches
// the payloads somebody thought of.
func quotesBalanced(stmt string) bool {
	inString := false
	for i := 0; i < len(stmt); i++ {
		switch stmt[i] {
		case '\\':
			if inString {
				// Skip the escaped character, whatever it is.
				i++
			}
		case '\'':
			inString = !inString
		}
	}
	return !inString
}

// The scanner above must actually be able to fail, or the test above proves
// nothing.
func TestQuotesBalancedDetectsInjection(t *testing.T) {
	if quotesBalanced(`SET type='start'; DROP TABLE photo; --'`) {
		// Two literals and a stray quote: this is what an unescaped payload looks
		// like, and it must be reported.
		t.Error("expected an unbalanced statement to be detected")
	}
	if !quotesBalanced(`SET type='start\'; DROP TABLE photo; --'`) {
		t.Error("expected a properly escaped statement to pass")
	}
}

func TestDatetimeNullForZero(t *testing.T) {
	if got := datetime(time.Time{}); got != "NULL" {
		t.Errorf("got %s, want NULL", got)
	}
	if got := datetime(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)); got != `'2026-09-07 12:00:00'` {
		t.Errorf("got %s", got)
	}
}

func TestRenditionsRoundTrip(t *testing.T) {
	in := []PhotoRendition{{Name: "thumb256", Ref: ref('b'), ContentType: "image/jpeg", Width: 256, Height: 192}}
	encoded, err := encodeRenditions(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := decodeRenditions(encoded)
	if len(out) != 1 || out[0].Ref != in[0].Ref || out[0].Width != 256 {
		t.Errorf("round trip lost data: %+v", out)
	}
	if encodeEmpty, _ := encodeRenditions(nil); encodeEmpty != "" {
		t.Errorf("expected an empty string for no renditions, got %q", encodeEmpty)
	}
	// A corrupt column degrades to "no renditions" rather than erroring: the
	// reader falls back to the display image.
	if got := decodeRenditions("{not json"); got != nil {
		t.Errorf("expected nil for a corrupt column, got %+v", got)
	}
}

func TestSmallestRenditionPrefersCheapest(t *testing.T) {
	got := smallestRendition([]PhotoRendition{
		{Name: "thumb1024", Ref: ref('a'), Width: 1024, Height: 768},
		{Name: "thumb256", Ref: ref('b'), Width: 256, Height: 192},
		{Name: "unknown", Ref: ref('c')},
	})
	if got.Name != "thumb256" {
		t.Errorf("got %q, want thumb256 — a rendition with unknown dimensions must not win by comparing zero", got.Name)
	}
}
