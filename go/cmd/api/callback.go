package main

// The camera app's webhook: the one way a photograph enters the ecosystem.
//
// # The response is the contract
//
// This handler answers only after the bytes are stored and the event is published.
// Nothing is acknowledged early. The reason is that the camera app is the only
// other copy of the photograph: on a non-2xx it appends the payload to its own
// `webhook-failed.jsonl` and keeps the file, so a failure here is recoverable and a
// premature success is not. `webhook-failed.jsonl` is, in effect, our retry queue,
// and answering 200 before the work is done throws it away.
//
// That single fact is also why there is no "pending photo" table in this service. An
// earlier design parked photographs it could not attribute and retried them later;
// once the camera app keeps the bytes and logs the failed payload, that table would
// be a second, worse copy of a queue that already exists.
//
// # Rejections are meant to be visible
//
// Only spejder patruljer are photographed, but test shots with invented team
// numbers do arrive. Those must not look like success and must not look like a
// transient error either, so they get their own status, their own machine-readable
// code, their own response header, and a WARN log line. See rejection below.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	bff "foto.nathejk.dk/cmd/api/app"
	"foto.nathejk.dk/internal/fetcher"
	"foto.nathejk.dk/internal/imaging"
	"foto.nathejk.dk/internal/teamnumber"
	"foto.nathejk.dk/nathejk/table/photo"
)

// kameraPayload is the webhook body, exactly as the camera app sends it.
//
// Field names follow that app's JSON and must not be "tidied": we have decided not
// to change that repo, so this struct is a description of somebody else's output,
// not a design of our own.
type kameraPayload struct {
	TeamNumber string `json:"teamNumber"`
	Type       string `json:"type"`
	Attention  bool   `json:"attention"`
	ImageURL   string `json:"imageUrl"`
	// CreatedAt is when the photograph was taken. The camera app sends RFC3339 with
	// an offset, which time.Time parses; a zero value means it was absent, and the
	// event then carries no capture time rather than an invented one.
	CreatedAt time.Time `json:"createdAt"`
}

// rejection is a refusal that is meant to be noticed.
type rejection struct {
	status int
	// code is machine-readable and stable, for whoever greps the retry log.
	code string
	// message is for a human reading a log line or a jsonl entry.
	message string
	// retryable distinguishes "this will never work" from "try again later". It
	// decides nothing about the status code — it is what the log line says, so an
	// operator knows whether replaying the entry is worth their time.
	retryable bool
	// fix names the thing to change when the cause is our configuration rather than
	// the request. Empty for rejections the caller can act on themselves.
	//
	// This field exists because of a real confusion its absence caused: a host missing
	// from the allowlist reported "imageUrl is not a fetchable address", which reads as
	// "your URL is broken" when the URL was perfectly good — fetchable with curl from
	// the same machine — and the allowlist was simply short. A rejection caused by our
	// own configuration has to say so.
	fix string
}

func (r rejection) Error() string { return fmt.Sprintf("%s: %s", r.code, r.message) }

// The rejection taxonomy. Distinct statuses so a failure can be diagnosed from an
// access log alone, without correlating it against ours.
var (
	// errUnknownTeam is the one this exists for: a photograph whose team number
	// matches no patrulje. Almost always a test shot or a typo.
	//
	// 422 rather than 404: the request is well-formed and the endpoint exists, so a
	// 404 would suggest the webhook URL was wrong and send somebody looking in the
	// wrong place. 422 says "understood, cannot process this".
	//
	// Rather than 2xx-and-park, deliberately. A test photograph that returned 200
	// would be indistinguishable from a real one in the camera app, and the odd
	// number would only ever be noticed by somebody auditing rows. Non-2xx means it
	// lands in webhook-failed.jsonl, where a wrong number is visible next to the
	// photograph it belongs to — and if it turns out to be a real team that had not
	// been projected yet, replaying that line then succeeds. Nothing is lost either
	// way, because the camera app still holds the file.
	errUnknownTeam = rejection{
		status: http.StatusUnprocessableEntity, code: "unknown_team_number",
		message: "no patrulje has this team number in this year", retryable: false,
	}

	// errAmbiguousTeam should be impossible: (year, teamNumber) is unique. It is
	// still checked, because the uniqueness is a convention rather than a database
	// constraint — idx_patrulje_year_number is a plain KEY — and the failure mode if
	// it ever breaks is attributing a child's photograph to the wrong team, silently.
	// 409 marks it as a data-integrity problem rather than a bad request.
	errAmbiguousTeam = rejection{
		status: http.StatusConflict, code: "ambiguous_team_number",
		message: "more than one patrulje has this team number in this year", retryable: false,
	}

	errBadPayload = rejection{
		status: http.StatusBadRequest, code: "bad_payload",
		message: "the webhook body could not be read", retryable: false,
	}
	errBadImageURL = rejection{
		status: http.StatusBadRequest, code: "bad_image_url",
		message: "imageUrl is not a usable http(s) URL", retryable: false,
	}
	// errHostNotAllowed is our configuration, not the caller's mistake, and used to be
	// indistinguishable from a broken URL. It gets its own code, names the host, and
	// names the setting to change.
	errHostNotAllowed = rejection{
		status: http.StatusBadRequest, code: "host_not_allowed",
		message:   "imageUrl's host is not in this service's allowlist, so it was refused without being fetched",
		retryable: false,
		fix:       "add the host to PHOTO_HOSTS and restart foto",
	}
	// errUpstreamNotFound is the genuine "that file is not there" case — a different
	// problem from both of the above, and worth not confusing with them: the path is in
	// kamera's payload but not on kamera's disk.
	errUpstreamNotFound = rejection{
		status: http.StatusBadRequest, code: "upstream_not_found",
		message:   "imageUrl returned 404 — the host is reachable but has no file at that path",
		retryable: false,
	}
	errNotAnImage = rejection{
		status: http.StatusBadRequest, code: "not_an_image",
		message: "the fetched bytes are not a decodable image", retryable: false,
	}
	errTooLarge = rejection{
		status: http.StatusRequestEntityTooLarge, code: "too_large",
		message: "the photo is larger than this service accepts", retryable: false,
	}
	// errFetchFailed is the upstream's fault, so it is retryable: the camera app's
	// own web server was unreachable or erroring when we tried to read the file back.
	errFetchFailed = rejection{
		status: http.StatusBadGateway, code: "fetch_failed",
		message: "the photo could not be fetched from imageUrl", retryable: true,
	}
	errNotReady = rejection{
		status: http.StatusServiceUnavailable, code: "not_ready",
		message: "the service cannot record a photo right now", retryable: true,
	}
	errStoreFailed = rejection{
		status: http.StatusInternalServerError, code: "store_failed",
		message: "the photo could not be stored", retryable: true,
	}
	errPublishFailed = rejection{
		status: http.StatusInternalServerError, code: "publish_failed",
		message: "the photo was stored but could not be announced", retryable: true,
	}
)

// Ingest limits. Deliberately generous on size — a modern phone photograph is
// several megabytes and refusing a real one is worse than accepting a large one —
// with the actual cap enforced by the fetcher from configuration.
const (
	displayEdge = 2000
	jpegQuality = 85
)

// thumbnailEdges is the rendition set. 2000px matches what the camera app already
// produced for its own use, so nothing downstream loses a size it had; 1024 and 256
// are the full-screen and grid sizes consumers actually ask for.
var thumbnailEdges = []int{1024, 256}

// kameraCallbackHandler ingests one photograph.
//
// @Summary      Camera app photo webhook
// @Description  Called by the camera app when a photo has been taken. The body carries a URL, not the bytes, so this service fetches the image, stores the original verbatim, derives the rendition set, and publishes NATHEJK.<year>.patrulje.<teamID>.photographed. It responds only once all of that has succeeded, because the caller's failed-webhook log is the only retry mechanism. Authenticated with a shared secret in X-Webhook-Secret.
// @Tags         callback
// @Accept       json
// @Produce      json
// @Param        payload  body      kameraPayload  true  "The camera app's notification"
// @Success      200      {object}  ingestResponse
// @Failure      400      {object}  map[string]string  "bad payload, bad imageUrl, or not a decodable image"
// @Failure      401      {object}  map[string]string  "missing or wrong X-Webhook-Secret"
// @Failure      409      {object}  map[string]string  "ambiguous_team_number — should be impossible"
// @Failure      413      {object}  map[string]string  "the photo is too large"
// @Failure      422      {object}  map[string]string  "unknown_team_number — most often a test photo"
// @Failure      502      {object}  map[string]string  "imageUrl could not be fetched"
// @Failure      503      {object}  map[string]string  "database or broker unavailable — retry"
// @Router       /callback/kamera [post]
func (app *application) kameraCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !app.webhookAuthorized(r) {
		app.Logger.Warn("rejected webhook with a bad secret", "ip", clientIP(r))
		app.AuthenticationRequiredResponse(w, r)
		return
	}

	var payload kameraPayload
	if err := app.ReadJSON(w, r, &payload); err != nil {
		app.rejectIngest(w, r, payload, errBadPayload, err)
		return
	}

	res, rej, err := app.ingest(r.Context(), payload)
	if rej != nil {
		app.rejectIngest(w, r, payload, *rej, err)
		return
	}

	app.Logger.Info("photographed",
		"teamNumber", payload.TeamNumber, "teamId", res.TeamID, "type", payload.Type,
		"ref", res.Ref, "bytes", res.Bytes)

	if err := app.WriteJSON(w, http.StatusOK, res, nil); err != nil {
		app.ServerErrorResponse(w, r, err)
	}
}

// ingestResponse is what a successful callback returns.
type ingestResponse struct {
	Status string `json:"status"`
	TeamID string `json:"teamId"`
	Year   string `json:"year"`
	Ref    string `json:"ref"`
	Bytes  int    `json:"bytes"`

	// Renditions lists the names that can be fetched, so the caller need not guess
	// the rendition vocabulary.
	Renditions []string `json:"renditions,omitempty"`
}

// ingest does the work, in the order that makes a failure survivable: resolve,
// fetch, decode, store bytes, then publish.
//
// Bytes before event, always. "Event published, bytes missing" is unrecoverable —
// every consumer sees a photograph that can never be fetched, permanently, on a log
// that cannot be edited. "Bytes stored, event not published" is a collectable
// orphan and one replay away from correct.
//
// Resolution comes first, before anything is fetched or stored, so a test shot with
// an invented number costs one indexed query rather than a download and three blob
// writes. It also means nothing is stored for a photograph we are about to refuse:
// unattributable pictures of children, with no team and no retention anchor, are
// not something to accumulate.
func (app *application) ingest(ctx context.Context, payload kameraPayload) (ingestResponse, *rejection, error) {
	if err := app.ready(); err != nil {
		return ingestResponse{}, &errNotReady, err
	}
	if payload.ImageURL == "" {
		return ingestResponse{}, &errBadPayload, errors.New("imageUrl is empty")
	}

	year := app.config.eventYear

	teamID, err := app.teams.TeamIDByNumber(ctx, year, payload.TeamNumber)
	switch {
	case errors.Is(err, teamnumber.ErrNotFound):
		return ingestResponse{}, &errUnknownTeam, err
	case errors.Is(err, teamnumber.ErrAmbiguous):
		return ingestResponse{}, &errAmbiguousTeam, err
	case err != nil:
		return ingestResponse{}, &errNotReady, err
	}

	raw, _, err := app.photos.Fetch(ctx, payload.ImageURL)
	if err != nil {
		return ingestResponse{}, fetchRejection(err), err
	}

	// The content type is decided by decoding, never by the header the upstream
	// sent: that string is the claim of the server we just fetched from.
	prepared, err := imaging.Prepare(raw, displayEdge, thumbnailEdges, jpegQuality)
	switch {
	case errors.Is(err, imaging.ErrTooLarge):
		return ingestResponse{}, &errTooLarge, err
	case err != nil:
		return ingestResponse{}, &errNotAnImage, err
	}

	stored, err := app.storePhoto(ctx, raw, prepared)
	if err != nil {
		return ingestResponse{}, &errStoreFailed, err
	}

	cmd := photo.Photographed{
		Year:       year,
		TeamID:     string(teamID),
		TeamNumber: payload.TeamNumber,
		Type:       payload.Type,
		Attention:  payload.Attention,
		Display:    stored.display,
		Renditions: stored.renditions,
		Original:   stored.original,
		Source: &photo.PhotoSource{
			URL:       payload.ImageURL,
			Kind:      "kamera-webhook",
			FetchedAt: time.Now().UTC(),
		},
		CapturedAt: payload.CreatedAt,
	}
	if err := app.photoTable.Publish(cmd); err != nil {
		return ingestResponse{}, &errPublishFailed, err
	}

	names := make([]string, 0, len(stored.renditions))
	for _, rendition := range stored.renditions {
		names = append(names, rendition.Name)
	}

	return ingestResponse{
		Status:     "photographed",
		TeamID:     string(teamID),
		Year:       year,
		Ref:        stored.display.Ref,
		Bytes:      stored.display.Bytes,
		Renditions: names,
	}, nil, nil
}

// fetchRejection maps a fetch failure onto the taxonomy.
//
// Three outcomes that were once one, and the distinction is the whole point:
//
//	host_not_allowed    our config — the URL is fine, we refused to fetch it
//	upstream_not_found  their disk — the host answered, the file is not there
//	fetch_failed        their server — unreachable or erroring, so retryable
//
// Collapsing these into one "bad_image_url" is what makes somebody curl the URL
// successfully and conclude the service is broken.
func fetchRejection(err error) *rejection {
	switch {
	case errors.Is(err, fetcher.ErrHostNotAllowed):
		return &errHostNotAllowed
	case errors.Is(err, fetcher.ErrInvalidURL),
		errors.Is(err, fetcher.ErrSchemeNotAllowed):
		return &errBadImageURL
	case errors.Is(err, fetcher.ErrTooLarge):
		return &errTooLarge
	case errors.Is(err, fetcher.ErrUnexpectedStatus):
		var status *fetcher.StatusError
		if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
			return &errUpstreamNotFound
		}
		return &errFetchFailed
	default:
		return &errFetchFailed
	}
}

// rejectIngest writes the refusal and makes sure it is noticeable.
//
// Three channels, on purpose. The status and the `code` in the body are for the
// caller and for whoever later greps its failed-webhook log. The
// X-Foto-Rejected header is for an access log, where a bare 422 among 200s is
// otherwise anonymous. The WARN log line is for us, and carries the team number and
// the URL, because "which photo was this?" is the first question anyone asks.
func (app *application) rejectIngest(w http.ResponseWriter, r *http.Request, payload kameraPayload, rej rejection, cause error) {
	level := app.Logger.Warn
	if rej.status >= 500 && rej.code != errNotReady.code {
		level = app.Logger.Error
	}
	level("rejected photo",
		"code", rej.code,
		"status", rej.status,
		"retryable", rej.retryable,
		"teamNumber", payload.TeamNumber,
		"type", payload.Type,
		"imageUrl", payload.ImageURL,
		"ip", clientIP(r),
		"err", cause,
	)
	if rej.fix != "" {
		// Repeated on its own line rather than buried in the body above, because this
		// is the line somebody will be reading when they want to know what to change.
		app.Logger.Warn("rejection is a configuration problem",
			"code", rej.code, "fix", rej.fix, "allowedHosts", app.config.photoHosts)
	}

	// A dedicated header so the refusal shows up in Traefik's access log without
	// anybody joining it to ours.
	w.Header().Set("X-Foto-Rejected", rej.code)

	body := map[string]any{
		"error":      rej.message,
		"code":       rej.code,
		"retryable":  rej.retryable,
		"teamNumber": payload.TeamNumber,
	}
	if rej.fix != "" {
		body["fix"] = rej.fix
	}
	// The offending host, echoed back for the one rejection where knowing it is the
	// whole fix. It is the host the caller sent, so this discloses nothing new — unlike
	// returning the allowlist itself, which is why that is logged at boot instead.
	if rej.code == errHostNotAllowed.code {
		if u, err := url.Parse(payload.ImageURL); err == nil && u.Hostname() != "" {
			body["host"] = u.Hostname()
		}
	}
	if err := app.WriteJSON(w, rej.status, body, nil); err != nil {
		app.ServerErrorResponse(w, r, err)
	}
}

// webhookAuthorized checks the shared secret.
//
// An empty configured secret disables the check, which is only ever right in dev —
// and is logged as a warning at startup rather than silently tolerated.
func (app *application) webhookAuthorized(r *http.Request) bool {
	if app.config.webhookSecret == "" {
		return true
	}
	// Constant-time comparison: the secret is fixed and an attacker can call this
	// endpoint as often as they like, which is exactly the situation a timing side
	// channel needs. The length check that subtle.ConstantTimeCompare requires is
	// itself not constant-time, which is fine — the length of a shared secret is not
	// the part worth hiding.
	given := r.Header.Get("X-Webhook-Secret")
	return subtle.ConstantTimeCompare([]byte(given), []byte(app.config.webhookSecret)) == 1
}

// clientIP is a free function rather than a method because the receiver in this
// file is named `app`, which shadows the imported transport package; the import is
// aliased to `bff` for the same reason.
func clientIP(r *http.Request) string { return bff.ClientIP(r) }
