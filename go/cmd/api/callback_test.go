package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jrgensen/cqrs"
	"github.com/jrgensen/cqrs/cqrstest"

	bff "foto.nathejk.dk/cmd/api/app"
	"foto.nathejk.dk/internal/blob"
	"foto.nathejk.dk/internal/fetcher"
	"foto.nathejk.dk/internal/teamnumber"
	"foto.nathejk.dk/nathejk/table/photo"
)

const testSecret = "s3cret"

// jpegBytes builds a real JPEG, since the ingest path decides what an image is by
// decoding it rather than by trusting a header.
func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

type harness struct {
	app       *application
	mock      sqlmock.Sqlmock
	writer    *cqrstest.Writer
	publisher *cqrstest.Publisher
	upstream  *httptest.Server
	// imageURL is a URL on the allowlisted upstream serving a valid JPEG.
	imageURL string
}

// newHarness wires an application with fakes for everything external.
func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{}

	// The upstream stands in for the camera app's own web server.
	mux := http.NewServeMux()
	mux.HandleFunc("/photo.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes(t, 1200, 900))
	})
	mux.HandleFunc("/notanimage.jpg", func(w http.ResponseWriter, r *http.Request) {
		// Lying about the content type on purpose: the ingest path must decide by
		// decoding, not by trusting this header.
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("this is definitely not a jpeg"))
	})
	mux.HandleFunc("/boom.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	})
	mux.HandleFunc("/missing.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	h.upstream = httptest.NewServer(mux)
	t.Cleanup(h.upstream.Close)

	upstreamURL, err := url.Parse(h.upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	h.imageURL = h.upstream.URL + "/photo.jpg"

	f, err := fetcher.New([]string{upstreamURL.Hostname()}, 32<<20, 5*time.Second)
	if err != nil {
		t.Fatalf("fetcher.New: %v", err)
	}
	f.Client = h.upstream.Client()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.mock = mock

	h.writer = &cqrstest.Writer{}
	h.publisher = &cqrstest.Publisher{}

	table, err := photo.New(h.publisher, h.writer, db)
	if err != nil {
		t.Fatalf("photo.New: %v", err)
	}

	// A connected broker, as far as ready() can tell.
	ev := &eventing{writer: nil, reader: db}
	var pub cqrs.Publisher = h.publisher
	ev.held.Store(&pub)

	h.app = &application{
		JsonApi: bff.JsonApi{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		config: config{
			eventYear:     "2026",
			webhookSecret: testSecret,
			photoHosts:    []string{upstreamURL.Hostname()},
			maxPhotoBytes: 32 << 20,
			fetchTimeout:  5 * time.Second,
		},
		eventing:   ev,
		teams:      teamnumber.NewResolver(db),
		photoTable: table,
		blobs:      blob.NewMemoryStore(),
		photos:     f,
	}
	return h
}

// expectTeamLookup queues the resolver's query. teamID empty means "no such team".
func (h *harness) expectTeamLookup(year, number string, teamIDs ...string) {
	rows := sqlmock.NewRows([]string{"teamId"})
	for _, id := range teamIDs {
		rows = rows.AddRow(id)
	}
	h.mock.ExpectQuery("SELECT teamId FROM patrulje").WithArgs(year, number).WillReturnRows(rows)
}

func (h *harness) post(t *testing.T, secret string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/callback/kamera", bytes.NewReader(body))
	if secret != "" {
		req.Header.Set("X-Webhook-Secret", secret)
	}
	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, req)
	return rec
}

// payload is the camera app's webhook body.
func (h *harness) payload(number, imageURL string) map[string]any {
	return map[string]any{
		"teamNumber": number,
		"type":       "start",
		"attention":  false,
		"imageUrl":   imageURL,
		"createdAt":  "2026-09-07T12:00:00.000+00:00",
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestCallbackStoresAndPublishes(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "42", "team-abc")

	rec := h.post(t, testSecret, h.payload("42", h.imageURL))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["status"] != "photographed" || body["teamId"] != "team-abc" {
		t.Errorf("unexpected body: %v", body)
	}

	// One event, on the team's subject.
	if got := h.publisher.Subjects(); len(got) != 1 || got[0] != "NATHEJK.2026.patrulje.team-abc.photographed" {
		t.Fatalf("unexpected published subjects: %v", got)
	}

	var event photo.PatruljePhotographed
	if err := h.publisher.Messages[0].Body(&event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.TeamNumber != "42" || event.Type != "start" {
		t.Errorf("provenance lost: %+v", event)
	}
	// The rendition set, and an original recorded alongside it.
	if len(event.Renditions) != len(thumbnailEdges) {
		t.Errorf("got %d renditions, want %d", len(event.Renditions), len(thumbnailEdges))
	}
	if event.Original == nil || event.Original.Ref == "" {
		t.Fatal("expected an original to be recorded")
	}
	if event.CapturedAt.IsZero() {
		t.Error("expected the camera app's createdAt to be carried")
	}

	// The bytes must be in the store before the event is published, so every ref the
	// event names must resolve.
	for _, ref := range append([]string{event.Ref, event.Original.Ref}, renditionRefs(event)...) {
		if ok, err := h.app.blobs.Exists(t.Context(), blob.Ref(ref)); err != nil || !ok {
			t.Errorf("event references ref %s which is not in the store (err=%v)", ref, err)
		}
	}
}

func renditionRefs(event photo.PatruljePhotographed) []string {
	out := make([]string, 0, len(event.Renditions))
	for _, r := range event.Renditions {
		out = append(out, r.Ref)
	}
	return out
}

// The stored original must be byte-identical to what the upstream served: nothing is
// stripped, re-encoded or normalised (PRD 001 §6).
func TestCallbackStoresOriginalVerbatim(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "42", "team-abc")

	rec := h.post(t, testSecret, h.payload("42", h.imageURL))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var event photo.PatruljePhotographed
	if err := h.publisher.Messages[0].Body(&event); err != nil {
		t.Fatalf("decode event: %v", err)
	}

	reader, err := h.app.blobs.Get(t.Context(), blob.Ref(event.Original.Ref))
	if err != nil {
		t.Fatalf("get original: %v", err)
	}
	defer func() { _ = reader.Close() }()
	stored, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	resp, err := h.upstream.Client().Get(h.imageURL)
	if err != nil {
		t.Fatalf("fetch upstream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	served, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read upstream: %v", err)
	}

	if !bytes.Equal(stored, served) {
		t.Errorf("the stored original is not byte-identical to the upload (%d vs %d bytes)",
			len(stored), len(served))
	}
	if event.Original.Bytes != len(served) {
		t.Errorf("recorded original size %d, actual %d", event.Original.Bytes, len(served))
	}
}

// THE requirement: a test photo with an odd team number must stand out. It must not
// look like success, and it must not look like a transient failure either.
func TestCallbackUnknownTeamNumberStandsOut(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "9999")

	rec := h.post(t, testSecret, h.payload("9999", h.imageURL))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Foto-Rejected"); got != "unknown_team_number" {
		t.Errorf("X-Foto-Rejected = %q, want unknown_team_number", got)
	}
	body := decodeBody(t, rec)
	if body["code"] != "unknown_team_number" {
		t.Errorf("code = %v, want unknown_team_number", body["code"])
	}
	if body["retryable"] != false {
		t.Errorf("retryable = %v, want false — replaying this will never help", body["retryable"])
	}
	// The number is echoed so the rejection is diagnosable from the response alone.
	if body["teamNumber"] != "9999" {
		t.Errorf("teamNumber = %v, want 9999", body["teamNumber"])
	}

	// Nothing was published, and nothing was stored: an unattributable photograph of
	// a child is not something to accumulate, and the camera app still has the file.
	if len(h.publisher.Messages) != 0 {
		t.Error("nothing should have been published for an unknown team")
	}
	if n := h.app.blobs.(*blob.MemoryStore).Len(); n != 0 {
		t.Errorf("%d objects stored for a rejected photo, want 0", n)
	}
}

// Resolution happens before the fetch, so a test shot costs one indexed query rather
// than a download and three blob writes.
func TestCallbackUnknownTeamDoesNotFetch(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "9999")

	var hits int
	h.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes(t, 100, 100))
	})

	if rec := h.post(t, testSecret, h.payload("9999", h.imageURL)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if hits != 0 {
		t.Errorf("the upstream was hit %d times for a photo we were going to refuse", hits)
	}
}

// Should be impossible — (year, teamNumber) is unique — but it is a convention, not a
// database constraint, and the failure mode is attributing a child's photograph to
// the wrong team.
func TestCallbackAmbiguousTeamNumberIsAConflict(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "42", "team-abc", "team-def")

	rec := h.post(t, testSecret, h.payload("42", h.imageURL))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Foto-Rejected"); got != "ambiguous_team_number" {
		t.Errorf("X-Foto-Rejected = %q", got)
	}
	if len(h.publisher.Messages) != 0 {
		t.Error("an ambiguous number must not be resolved by guessing")
	}
}

// Each rejection gets its own status so a failure is diagnosable from an access log
// alone, and each carries the header that makes it visible there.
func TestCallbackRejectionTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
		wantCode   string
		retryable  bool
	}{
		{"undecodable bytes", "/notanimage.jpg", http.StatusBadRequest, "not_an_image", false},
		{"upstream 500", "/boom.jpg", http.StatusBadGateway, "fetch_failed", true},
		{"upstream 404", "/missing.jpg", http.StatusBadRequest, "upstream_not_found", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.expectTeamLookup("2026", "42", "team-abc")

			rec := h.post(t, testSecret, h.payload("42", h.upstream.URL+tc.path))

			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("X-Foto-Rejected"); got != tc.wantCode {
				t.Errorf("X-Foto-Rejected = %q, want %q", got, tc.wantCode)
			}
			body := decodeBody(t, rec)
			if body["retryable"] != tc.retryable {
				t.Errorf("retryable = %v, want %v", body["retryable"], tc.retryable)
			}
			if len(h.publisher.Messages) != 0 {
				t.Error("nothing should have been published")
			}
		})
	}
}

// A URL on a host we do not serve photos from is refused before any request is made.
// The webhook body is attacker-influenceable, so this is the SSRF boundary.
//
// It must also be *distinguishable from a broken URL*. Reporting the config problem as
// "imageUrl is not a fetchable address" is what makes somebody curl the URL
// successfully and conclude the service is broken, so the code, the offending host and
// the fix are all asserted here.
func TestCallbackRefusesForeignHost(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "42", "team-abc")

	rec := h.post(t, testSecret, h.payload("42", "https://kamera.example.com/photos/start/Team-2_1.jpg"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Foto-Rejected"); got != "host_not_allowed" {
		t.Errorf("X-Foto-Rejected = %q, want host_not_allowed", got)
	}
	body := decodeBody(t, rec)
	if body["code"] != "host_not_allowed" {
		t.Errorf("code = %v, want host_not_allowed", body["code"])
	}
	// The host, so the reader knows which one to allowlist.
	if body["host"] != "kamera.example.com" {
		t.Errorf("host = %v, want kamera.example.com", body["host"])
	}
	// And the setting to change, so this is not a guessing game.
	if fix, _ := body["fix"].(string); !strings.Contains(fix, "PHOTO_HOSTS") {
		t.Errorf("fix = %q, want it to name PHOTO_HOSTS", fix)
	}
}

// A host not on the allowlist must be refused without a request leaving the process:
// that is what makes this an SSRF boundary rather than a filter on the response.
//
// The allowlist is narrowed here rather than pointing at a second server, because
// httptest always binds 127.0.0.1 — so a "foreign" test server is the *same host* on a
// different port, and the allowlist matches hostnames, not ports. (That is itself worth
// knowing: allowlisting a host permits any port on it.)
func TestCallbackForeignHostIsNotFetched(t *testing.T) {
	h := newHarness(t)
	h.expectTeamLookup("2026", "42", "team-abc")

	var hits int
	h.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(jpegBytes(t, 100, 100))
	})

	// An allowlist that does not cover the upstream, while still pointing the client
	// at it: if the check were performed anywhere but before the request, the server
	// would be hit.
	narrowed, err := fetcher.New([]string{"kamera.nathejk.dk"}, 32<<20, 5*time.Second)
	if err != nil {
		t.Fatalf("fetcher.New: %v", err)
	}
	narrowed.Client = h.upstream.Client()
	h.app.photos = narrowed

	rec := h.post(t, testSecret, h.payload("42", h.imageURL))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Foto-Rejected"); got != "host_not_allowed" {
		t.Errorf("X-Foto-Rejected = %q, want host_not_allowed", got)
	}
	if hits != 0 {
		t.Errorf("the upstream was contacted %d times; a disallowed host must be refused before any request", hits)
	}
}

// A malformed URL is a different problem from a disallowed host and keeps the old code.
func TestCallbackRejectsUnusableURL(t *testing.T) {
	for _, bad := range []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"not a url at all",
	} {
		h := newHarness(t)
		h.expectTeamLookup("2026", "42", "team-abc")

		rec := h.post(t, testSecret, h.payload("42", bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: got %d, want 400", bad, rec.Code)
			continue
		}
		if got := rec.Header().Get("X-Foto-Rejected"); got != "bad_image_url" && got != "host_not_allowed" {
			t.Errorf("%q: X-Foto-Rejected = %q", bad, got)
		}
	}
}

func TestCallbackRequiresSecret(t *testing.T) {
	for _, tc := range []struct{ name, secret string }{
		{"missing", ""},
		{"wrong", "not-the-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.post(t, tc.secret, h.payload("42", h.imageURL))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401", rec.Code)
			}
			if len(h.publisher.Messages) != 0 {
				t.Error("an unauthenticated call must not publish")
			}
		})
	}
}

func TestCallbackRejectsEmptyImageURL(t *testing.T) {
	h := newHarness(t)
	rec := h.post(t, testSecret, h.payload("42", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Foto-Rejected"); got != "bad_payload" {
		t.Errorf("X-Foto-Rejected = %q, want bad_payload", got)
	}
}

// With no broker the photograph cannot be announced, so the honest answer is a
// retryable 503 — the camera app then logs the payload and it can be replayed.
// Answering 200 here would lose the photograph silently.
func TestCallbackWithoutBrokerIsRetryable(t *testing.T) {
	h := newHarness(t)
	h.app.eventing = &eventing{}

	rec := h.post(t, testSecret, h.payload("42", h.imageURL))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["retryable"] != true {
		t.Errorf("retryable = %v, want true", body["retryable"])
	}
}

// GET must not ingest: a link preview or a crawler would otherwise store photographs.
func TestCallbackRejectsGet(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/callback/kamera", nil)
	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", rec.Code)
	}
}

// The health endpoint separates liveness from readiness, so a container can be up
// while its dependencies are not.
func TestHealthcheckReportsReadiness(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/api/healthcheck", nil)
	rec := httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, req)

	body := decodeBody(t, rec)
	if body["status"] != "available" || body["ready"] != true {
		t.Errorf("expected available and ready, got %v", body)
	}

	h.app.eventing = &eventing{}
	rec = httptest.NewRecorder()
	h.app.routes().ServeHTTP(rec, req)
	body = decodeBody(t, rec)
	if body["ready"] != false {
		t.Errorf("expected ready=false without a broker, got %v", body)
	}
	if !strings.Contains(fmt.Sprint(body["notReady"]), "publisher") {
		t.Errorf("expected notReady to name the missing piece, got %v", body["notReady"])
	}
}
