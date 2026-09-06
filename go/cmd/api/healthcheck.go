package main

import (
	"net/http"

	"foto.nathejk.dk/internal/vcs"
)

// healthcheckResponse is the body of GET /api/healthcheck.
type healthcheckResponse struct {
	Status      string `json:"status"`
	Environment string `json:"environment"`
	Version     string `json:"version"`

	// Ready reports whether a photo can be accepted right now. Distinct from
	// Status on purpose: the process can be alive and serving while the broker or
	// the database is still coming up, and conflating the two would either fail
	// the container's healthcheck during a normal start or claim readiness the
	// ingest path would then refuse.
	Ready bool `json:"ready"`

	// NotReady names what is missing. Present only when Ready is false.
	NotReady string `json:"notReady,omitempty"`
}

// healthcheckHandler reports liveness, and separately readiness.
//
// @Summary      Service health
// @Description  Liveness and readiness. `status` is always "available" if the process answers; `ready` reports whether the ingest pipeline has everything it needs (a database for the patrulje projection, and a broker to publish to).
// @Tags         ops
// @Produce      json
// @Success      200  {object}  healthcheckResponse
// @Router       /healthcheck [get]
func (app *application) healthcheckHandler(w http.ResponseWriter, r *http.Request) {
	res := healthcheckResponse{
		Status:      "available",
		Environment: app.config.env,
		Version:     vcs.Version(),
		Ready:       true,
	}
	if err := app.ready(); err != nil {
		res.Ready = false
		res.NotReady = err.Error()
	}

	if err := app.WriteJSON(w, http.StatusOK, res, nil); err != nil {
		app.ServerErrorResponse(w, r, err)
	}
}
