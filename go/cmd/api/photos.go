package main

// Serving photograph bytes.
//
// # Why a lookup instead of "if the store has it, send it"
//
// Refs are content hashes, so a URL is unguessable — but unguessable is not the
// same as authorized, and this service stores one class of object that must never
// leave it: the original, which keeps the upload's EXIF and therefore possibly the
// location a child was photographed in.
//
// So the read model decides, not the blob store. photo.Servable admits a ref only
// if it is some photograph's display image or one of its renditions. An original's
// ref is in the same store, under an equally valid hash, and is refused. That makes
// "don't serve originals" a property of the routing rather than a rule each handler
// has to remember.

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/julienschmidt/httprouter"

	"foto.nathejk.dk/internal/blob"
	"foto.nathejk.dk/nathejk/table/photo"
)

// showPhotoHandler serves one object's bytes.
//
// Registered for both GET and HEAD. net/http suppresses a response body for HEAD on
// its own, but this handler returns before reading the object rather than relying on
// that: streaming several megabytes off disk only to have them discarded is a
// pointless cost on a route a cache may probe often.
//
// @Summary      Serve photo bytes
// @Description  Serves a stored rendition by its content hash. Only display images and their renditions are servable; stored originals are refused with 404 because they retain the upload's metadata. The URL is a content hash, so the response is immutable and cached aggressively.
// @Tags         photos
// @Produce      image/jpeg
// @Param        ref  path      string  true  "sha256 content hash, 64 lowercase hex characters"
// @Success      200  {file}    binary
// @Failure      404  {object}  map[string]string  "unknown ref, or a ref this service will not serve"
// @Failure      503  {object}  map[string]string  "database unavailable"
// @Router       /photos/{ref} [get]
func (app *application) showPhotoHandler(w http.ResponseWriter, r *http.Request) {
	if app.photoTable == nil || app.blobs == nil {
		app.ServiceUnavailableResponse(w, r, "photo storage is unavailable")
		return
	}

	ref := strings.TrimSpace(httprouter.ParamsFromContext(r.Context()).ByName("ref"))

	// The read model is the authority on whether this may be served, and it also
	// supplies the content type — so the type comes from what was recorded at
	// ingest, not from sniffing the bytes on every request.
	contentType, err := app.photoTable.Servable(r.Context(), ref)
	switch {
	case errors.Is(err, photo.ErrNotFound):
		app.NotFoundResponse(w, r)
		return
	case err != nil:
		app.ServerErrorResponse(w, r, err)
		return
	}

	reader, err := app.blobs.Get(r.Context(), blob.Ref(ref))
	if errors.Is(err, blob.ErrNotFound) {
		// The row references bytes that are gone. A 404 is the right degradation:
		// "no photo" rather than a broken page. Logged as a warning because it means
		// the blob store and the read model disagree, which is worth knowing about —
		// the store is the half that cannot be rebuilt.
		app.Logger.Warn("read model references missing bytes", "ref", ref)
		app.NotFoundResponse(w, r)
		return
	}
	if err != nil {
		app.ServerErrorResponse(w, r, err)
		return
	}
	defer func() { _ = reader.Close() }()

	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	// Immutable and effectively forever: the URL is the hash of the bytes, so the
	// response for a given URL can never change. This is the whole practical payoff
	// of content addressing — no cache invalidation to get wrong.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	// These are photographs of identifiable minors. Even though the URL is
	// unguessable, they should not leak through a referrer or be sniffed into
	// something executable.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.Method == http.MethodHead {
		// Headers only. Deliberately no Content-Length: the store's seam is an
		// io.ReadCloser with no size on it, and the projection's recorded byte count
		// describes the object at ingest — close enough to be tempting, and wrong the
		// moment they disagree. Omitting it is honest; announcing a length the GET
		// might not match would break a cache in a way nobody would trace back here.
		return
	}

	if _, err := io.Copy(w, reader); err != nil {
		// Too late for a status code — the header is already written — so this is
		// only ever a log line. Usually a client that went away mid-download.
		app.Logger.Warn("writing photo bytes", "ref", ref, "err", err)
	}
}

// listTeamPhotosHandler lists a team's photographs.
//
// @Summary      A team's photos
// @Description  Every photograph recorded for a patrulje in the configured season, newest first, with each rendition's ref and dimensions so a client can choose a size without fetching one to measure it. Stored originals are not included.
// @Tags         photos
// @Produce      json
// @Param        teamId  path      string  true  "the patrulje's teamId"
// @Success      200     {object}  map[string]any
// @Failure      503     {object}  map[string]string  "database unavailable"
// @Router       /patruljer/{teamId}/photos [get]
func (app *application) listTeamPhotosHandler(w http.ResponseWriter, r *http.Request) {
	if app.photoTable == nil {
		app.ServiceUnavailableResponse(w, r, "the photo read model is unavailable")
		return
	}

	teamID := strings.TrimSpace(httprouter.ParamsFromContext(r.Context()).ByName("teamId"))

	photos, err := app.photoTable.ByTeam(r.Context(), app.config.eventYear, teamID)
	if errors.Is(err, photo.ErrNotFound) {
		app.NotFoundResponse(w, r)
		return
	}
	if err != nil {
		app.ServerErrorResponse(w, r, err)
		return
	}

	// An empty list rather than a 404 for a team with no photographs: "this team
	// exists and has none" and "no such team" are different answers, and only the
	// patrulje projection can distinguish them.
	body := map[string]any{
		"year":   app.config.eventYear,
		"teamId": teamID,
		"photos": photos,
	}
	if err := app.WriteJSON(w, http.StatusOK, body, nil); err != nil {
		app.ServerErrorResponse(w, r, err)
	}
}
