package main

import (
	"net/http"

	"github.com/julienschmidt/httprouter"
)

// routes registers the HTTP handlers.
//
// Three groups, and the split is deliberate:
//
//	/api/...       JSON, for internal consumers
//	/callback/...  the camera app's webhook — outside our trust boundary, secret-authed
//	/photos/...    raw bytes, addressed by content hash
//
// /photos is not under /api because a content-hash URL is meant to be pasted into an
// <img src>, and because it returns image bytes rather than JSON — different enough
// that grouping it with the JSON API would mislead.
//
// There is no SPA fallback and no static file server: foto is headless by decision
// (PRD 001 §7), so an unknown path is a JSON 404 rather than an index.html.
func (app *application) routes() http.Handler {
	router := httprouter.New()

	router.NotFound = http.HandlerFunc(app.NotFoundResponse)
	router.MethodNotAllowed = http.HandlerFunc(app.MethodNotAllowedResponse)

	router.HandlerFunc(http.MethodGet, "/api/healthcheck", app.healthcheckHandler)
	router.HandlerFunc(http.MethodGet, "/api/patruljer/:teamId/photos", app.listTeamPhotosHandler)

	// The ingest path. POST only: this is a fact being reported, and a GET that
	// stored a photograph would be fetched by every crawler and link preview.
	router.HandlerFunc(http.MethodPost, "/callback/kamera", app.kameraCallbackHandler)

	router.HandlerFunc(http.MethodGet, "/photos/:ref", app.showPhotoHandler)
	// HEAD as well as GET. httprouter does not derive one from the other, and this
	// route serves images to browsers and sits behind a caching proxy — both of which
	// legitimately probe with HEAD, and both of which would otherwise get a 405.
	router.HandlerFunc(http.MethodHead, "/photos/:ref", app.showPhotoHandler)

	return router
}
