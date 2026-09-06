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
//	/callback/...  kamera's webhook — outside our trust boundary, secret-authed
//	/photos/...    raw bytes, content-addressed
//
// There is no SPA fallback and no static file server: foto is headless by
// decision (PRD 001 §7), so an unknown path is a 404 rather than an index.html.
func (app *application) routes() http.Handler {
	router := httprouter.New()

	router.NotFound = http.HandlerFunc(app.NotFoundResponse)
	router.MethodNotAllowed = http.HandlerFunc(app.MethodNotAllowedResponse)

	router.HandlerFunc(http.MethodGet, "/api/healthcheck", app.healthcheckHandler)

	return router
}
