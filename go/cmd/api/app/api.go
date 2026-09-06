// Package app holds the transport-layer helpers shared by every handler.
//
// Embed JsonApi on the application struct to inherit them; handlers then call
// app.WriteJSON / app.ServerErrorResponse rather than reaching for
// json.NewEncoder or http.Error by hand, so error bodies have one shape.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// JsonApi supplies JSON encoding, request decoding and the standard error
// responses.
type JsonApi struct {
	Logger *slog.Logger
}

// Envelope is the shape of every JSON body this API returns.
type Envelope map[string]any

// WriteJSON sends status and data, with optional extra headers.
func (a *JsonApi) WriteJSON(w http.ResponseWriter, status int, data any, headers http.Header) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	body = append(body, '\n')

	for key, values := range headers {
		w.Header()[key] = values
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

// ReadJSON decodes the request body into dst, rejecting anything malformed with
// an error fit to show a client.
//
// DisallowUnknownFields is deliberately NOT set. This API's one inbound body is
// kamera's webhook payload, and kamera is a repo we have decided not to change:
// if it ever grows a field, rejecting the whole callback over it would drop
// photographs. Unknown fields are ignored instead.
func (a *JsonApi) ReadJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	maxBytes := 1_048_576
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError
		var invalidUnmarshalError *json.InvalidUnmarshalError
		var maxBytesError *http.MaxBytesError

		switch {
		case errors.As(err, &syntaxError):
			return fmt.Errorf("body contains badly-formed JSON (at character %d)", syntaxError.Offset)
		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("body contains badly-formed JSON")
		case errors.As(err, &unmarshalTypeError):
			if unmarshalTypeError.Field != "" {
				return fmt.Errorf("body contains incorrect JSON type for field %q", unmarshalTypeError.Field)
			}
			return fmt.Errorf("body contains incorrect JSON type (at character %d)", unmarshalTypeError.Offset)
		case errors.Is(err, io.EOF):
			return errors.New("body must not be empty")
		case errors.As(err, &maxBytesError):
			return fmt.Errorf("body must not be larger than %d bytes", maxBytesError.Limit)
		case errors.As(err, &invalidUnmarshalError):
			panic(err)
		default:
			return err
		}
	}

	// A second decode must hit EOF, or the body held more than one JSON value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must only contain a single JSON value")
	}
	return nil
}

// errorResponse writes a JSON error, falling back to a bare 500 if even that
// fails.
func (a *JsonApi) errorResponse(w http.ResponseWriter, r *http.Request, status int, message any) {
	if err := a.WriteJSON(w, status, Envelope{"error": message}, nil); err != nil {
		a.log(r, "writing error response", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// ServerErrorResponse logs the error and returns a generic 500.
//
// The detail is logged, never sent: an internal message can name a table, a host
// or a file path, and this API answers a webhook from outside.
func (a *JsonApi) ServerErrorResponse(w http.ResponseWriter, r *http.Request, err error) {
	a.log(r, "server error", err)
	a.errorResponse(w, r, http.StatusInternalServerError,
		"the server encountered a problem and could not process your request")
}

// NotFoundResponse returns a 404.
func (a *JsonApi) NotFoundResponse(w http.ResponseWriter, r *http.Request) {
	a.errorResponse(w, r, http.StatusNotFound, "the requested resource could not be found")
}

// MethodNotAllowedResponse returns a 405.
func (a *JsonApi) MethodNotAllowedResponse(w http.ResponseWriter, r *http.Request) {
	a.errorResponse(w, r, http.StatusMethodNotAllowed,
		fmt.Sprintf("the %s method is not supported for this resource", r.Method))
}

// BadRequestResponse returns a 400 carrying err's message.
func (a *JsonApi) BadRequestResponse(w http.ResponseWriter, r *http.Request, err error) {
	a.errorResponse(w, r, http.StatusBadRequest, err.Error())
}

// AuthenticationRequiredResponse returns a 401.
func (a *JsonApi) AuthenticationRequiredResponse(w http.ResponseWriter, r *http.Request) {
	a.errorResponse(w, r, http.StatusUnauthorized, "you must be authenticated to access this resource")
}

// ServiceUnavailableResponse returns a 503.
//
// Its own helper because it is the honest answer when the event stream is down:
// the photo cannot be recorded, and kamera must be told so it lands in
// webhook-failed.jsonl rather than being treated as delivered.
func (a *JsonApi) ServiceUnavailableResponse(w http.ResponseWriter, r *http.Request, message string) {
	if message == "" {
		message = "the service is temporarily unavailable, please retry"
	}
	a.errorResponse(w, r, http.StatusServiceUnavailable, message)
}

func (a *JsonApi) log(r *http.Request, msg string, err error) {
	if a.Logger == nil {
		return
	}
	attrs := []any{"err", err}
	if r != nil {
		attrs = append(attrs, "method", r.Method, "uri", r.URL.RequestURI())
	}
	a.Logger.Error(msg, attrs...)
}

// ClientIP returns a best-effort client address for logging.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	return r.RemoteAddr
}
