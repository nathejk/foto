package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"foto.nathejk.dk/nathejk/table/photo"
)

// ingestOne runs a photograph through the callback and returns the resulting event,
// so the serving tests work from refs the pipeline actually produced rather than
// from fixtures that could drift from it.
func ingestOne(t *testing.T, h *harness) photo.PatruljePhotographed {
	t.Helper()
	h.expectTeamLookup("2026", "42", "team-abc")

	if rec := h.post(t, testSecret, h.payload("42", h.imageURL)); rec.Code != http.StatusOK {
		t.Fatalf("ingest failed: %d %s", rec.Code, rec.Body.String())
	}

	var event photo.PatruljePhotographed
	if err := h.publisher.Messages[0].Body(&event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return event
}

// serveReady points the read model at rows matching the ingested event.
//
// The projection writes through cqrs.Writer, which in these tests is a fake that
// records statements rather than a database, so the read side has to be told what the
// row looks like. Kept explicit for that reason: it is the seam between the two fakes.
func expectServable(h *harness, ref, contentType string) {
	h.mock.ExpectQuery("SELECT contentType FROM photo WHERE ref = ?").
		WithArgs(ref).
		WillReturnRows(sqlmockRows("contentType", contentType))
}

func expectNotServableAsDisplay(h *harness, ref string) {
	h.mock.ExpectQuery("SELECT contentType FROM photo WHERE ref = ?").
		WithArgs(ref).
		WillReturnRows(sqlmockRows("contentType"))
}

func TestServeDisplayImage(t *testing.T) {
	h := newHarness(t)
	event := ingestOne(t, h)
	expectServable(h, event.Ref, "image/jpeg")

	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photos/"+event.Ref, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q", got)
	}
	// The URL is a content hash, so the response can never change. That is the whole
	// practical payoff of content addressing.
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if rec.Body.Len() != event.Bytes {
		t.Errorf("served %d bytes, event says %d", rec.Body.Len(), event.Bytes)
	}
}

// The invariant the whole keep-everything decision rests on: originals hold the
// upload's metadata — possibly the GPS coordinates of where a child was photographed
// — and no route may return one. Enforced by the read model rather than by each
// handler remembering, so this test is the guard on that rule.
func TestServeRefusesTheOriginal(t *testing.T) {
	h := newHarness(t)
	event := ingestOne(t, h)

	if event.Original == nil || event.Original.Ref == "" {
		t.Fatal("expected an original to have been stored")
	}

	// The bytes really are in the store; being unreachable is a policy decision, not
	// an accident of them being absent.
	if ok, err := h.app.blobs.Exists(t.Context(), blobRef(event.Original.Ref)); err != nil || !ok {
		t.Fatalf("the original should be stored (ok=%v err=%v)", ok, err)
	}

	// Not a display ref, and not in any row's rendition set.
	expectNotServableAsDisplay(h, event.Original.Ref)
	h.mock.ExpectQuery("SELECT renditions FROM photo").
		WillReturnRows(sqlmockRows("renditions"))

	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/photos/"+event.Original.Ref, nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 — an original must never be servable: %s",
			rec.Code, rec.Body.String())
	}
}

// A hash the blob store happens to hold but no row references must not be served
// either: otherwise learning any hash would be enough to fetch it.
func TestServeRefusesUnreferencedRef(t *testing.T) {
	h := newHarness(t)

	ref, err := h.app.blobs.Put(t.Context(), []byte("some bytes nothing references"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	expectNotServableAsDisplay(h, ref.String())
	h.mock.ExpectQuery("SELECT renditions FROM photo").
		WillReturnRows(sqlmockRows("renditions"))

	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photos/"+ref.String(), nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

// A malformed ref must be refused without touching the filesystem. "../../etc/passwd"
// is a ref-shaped string, and this ref reaches both a SQL statement and a file path.
func TestServeRefusesMalformedRef(t *testing.T) {
	for _, bad := range []string{
		strings.Repeat("g", 64),
		strings.Repeat("A", 64),
		"short",
	} {
		h := newHarness(t)
		rec := httptest.NewRecorder()
		h.app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/photos/"+bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("ref %q: got %d, want 404", bad, rec.Code)
		}
	}
}

// Regression: httprouter does not derive HEAD from GET, so this route answered 405 to
// every cache and browser that probed it — with none of the caching headers.
func TestServeSupportsHead(t *testing.T) {
	h := newHarness(t)
	event := ingestOne(t, h)
	expectServable(h, event.Ref, "image/jpeg")

	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/photos/"+event.Ref, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("HEAD must carry the same caching headers as GET, got %q", got)
	}
	// The handler returns before reading the object, so nothing is streamed off disk
	// only to be discarded.
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned a %d-byte body", rec.Body.Len())
	}
}

func TestListTeamPhotos(t *testing.T) {
	h := newHarness(t)
	event := ingestOne(t, h)

	h.mock.ExpectQuery("SELECT year, teamId, teamNumber, type, attention, ref").
		WithArgs("2026", "team-abc").
		WillReturnRows(photoRow(event))

	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/patruljer/team-abc/photos", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Year   string        `json:"year"`
		TeamID string        `json:"teamId"`
		Photos []photo.Photo `json:"photos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Year != "2026" || body.TeamID != "team-abc" || len(body.Photos) != 1 {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.Photos[0].Ref != event.Ref {
		t.Errorf("ref = %q, want %q", body.Photos[0].Ref, event.Ref)
	}
	// Each rendition's dimensions travel with it, so a client can pick a size without
	// fetching one to measure it.
	if len(body.Photos[0].Renditions) == 0 || body.Photos[0].Renditions[0].Width == 0 {
		t.Errorf("expected renditions with dimensions, got %+v", body.Photos[0].Renditions)
	}

	// The response must not mention the original, whatever the row holds.
	if strings.Contains(rec.Body.String(), event.Original.Ref) {
		t.Error("the listing leaked the original's ref")
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "original") {
		t.Error("the listing mentions an original")
	}
}

// A team with no photographs is an empty list, not a 404: "exists and has none" and
// "no such team" are different answers, and only the patrulje projection can tell
// them apart.
func TestListTeamPhotosEmpty(t *testing.T) {
	h := newHarness(t)

	h.mock.ExpectQuery("SELECT year, teamId, teamNumber, type, attention, ref").
		WithArgs("2026", "team-nobody").
		WillReturnRows(photoRow())

	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/patruljer/team-nobody/photos", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"photos":[]`) {
		t.Errorf("expected an empty list, got %s", rec.Body.String())
	}
}
