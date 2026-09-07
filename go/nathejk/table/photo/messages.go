// Package photo owns the photograph write path: the events, the subjects they
// are published on, and the projection that folds them into a read model.
//
// # Bound for shared-go
//
// This package is written to be lifted into github.com/nathejk/shared-go
// unchanged once it is stable. Two rules follow, and both are load-bearing:
//
//   - It may not import foto.nathejk.dk/internal/... . Go forbids importing
//     another module's internal tree, so such an import would block the move
//     outright. That is why validPhotoRef below duplicates internal/blob's
//     Ref.Valid instead of calling it.
//   - Its only non-stdlib dependency is github.com/jrgensen/cqrs, which supplies
//     Publisher, Writer, Reader, Message and Subject. Nothing here names
//     JetStream, a SQL driver, or an HTTP client.
//
// # Why the event type lives here rather than in internal/
//
// Every other event this service consumes is defined in shared-go, because
// another service publishes it. The photograph is the first event foto publishes
// itself, so somebody has to own the shape — and it is owned by the projection
// that consumes it, which cmd/api already imports in order to publish.
//
// The alternative was a struct in internal/photo plus a private copy here, since
// this package may not import internal/... . Two structs that must agree on their
// JSON tags, with nothing to catch it when they stop agreeing, is a worse trade
// than one exported type.
//
// # What is on the stream and what is not
//
// The event carries a *reference*, never bytes. Image payloads make replay
// expensive and put an opaque blob in a log optimised for small messages. The
// bytes live in a content-addressed store, which is consequently the only thing
// in this service that cannot be rebuilt from the log — and therefore the only
// thing that must be backed up.
//
// Content addressing is what makes the whole thing idempotent: a replay
// re-publishes the same refs, storing identical bytes is a no-op, and this
// projection converges on the same rows without anybody re-uploading anything.
package photo

import "time"

// PhotoRendition is one downscaled version of a photograph, stored as its own
// content-addressed object.
//
// A list of these rather than a single thumbRef: more sizes are expected — an
// overview grid wants a different size from a full-screen view — and this event
// is on an append-only log, so retrofitting a list later would mean two shapes to
// interpret forever. hej's portrait event learned this the expensive way and
// still carries a deprecated single-ref field for the sake of events published
// before the list existed.
//
// Each rendition carries its own Bytes/Width/Height, which is the point of the
// list. A consumer laying out several hundred teams has to answer "how much am I
// about to download?" before downloading it, and one that must fetch an object to
// learn its size cannot budget.
type PhotoRendition struct {
	// Name identifies the rendition, e.g. "thumb256", derived from its longest
	// edge. Derived rather than a label like "small", because a label needs a
	// table somewhere to say what it means, and that table is what drifts from
	// the pixels.
	Name string `json:"name"`

	// Ref is the content hash of this rendition's bytes.
	Ref string `json:"ref"`

	ContentType string `json:"contentType"`
	Bytes       int    `json:"bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// PhotoOriginal is the uploaded file exactly as received.
//
// **Nothing is stripped.** The stored bytes are byte-identical to what the camera
// app served, metadata and all: EXIF, IPTC, XMP, ICC. That is a deliberate
// product decision (PRD 001 §6) on the grounds that the archive is the canonical
// copy of the photograph, and that the camera app writes IPTC credit and
// copyright tags it would be wrong to discard.
//
// The consequence has to be stated wherever this type is read: **the original may
// carry the GPS coordinates of where a child was photographed**, so an original
// is never served to a consumer. What is served is always a rendition, which
// cannot carry metadata because it was re-encoded from pixels. That asymmetry —
// not a strip step somebody has to remember — is what contains the problem.
type PhotoOriginal struct {
	// Ref is the content hash of the stored original.
	Ref string `json:"ref"`

	// ContentType is the upload's own format, since the bytes were not
	// re-encoded. It can therefore differ from the renditions' image/jpeg.
	ContentType string `json:"contentType"`

	Bytes int `json:"bytes"`

	// Width and Height describe the stored bytes **before** rotation is applied.
	// They are therefore swapped relative to the renditions for a photo taken
	// sideways — see Orientation.
	Width  int `json:"width"`
	Height int `json:"height"`

	// Orientation is the EXIF orientation the file declares (1–8, 1 meaning
	// upright).
	//
	// It is still in the file, since nothing was stripped. It is recorded here
	// anyway for two reasons: a consumer can then reason about the photograph
	// without fetching an original it is not allowed to fetch, and the rotation
	// applied to the renditions becomes auditable from the log rather than being
	// a property of bytes nobody re-examines.
	Orientation int `json:"orientation"`
}

// PhotoSource records where the bytes came from.
//
// Provenance matters more here than for an ordinary upload, because this service
// does not receive the bytes: the camera app's webhook carries a URL, and foto
// fetches it. So the photograph's origin is a fact about a third party's web
// server at a moment in time, and it is the only thing that makes "why does this
// photo look wrong" answerable later.
type PhotoSource struct {
	// URL is the location the bytes were fetched from.
	URL string `json:"url"`

	// Kind names the ingest path, e.g. "kamera-webhook". Free text: a second
	// ingest path is plausible and an enum would have to be extended in lockstep
	// across every consumer.
	Kind string `json:"kind,omitempty"`

	// FetchedAt is when the bytes were actually retrieved, which is not
	// CapturedAt: a photograph replayed from the camera app's failed-webhook log
	// is fetched days after it was taken.
	FetchedAt time.Time `json:"fetchedAt,omitempty"`
}

// PatruljePhotographed says that a patrulje has been photographed.
//
// "Photographed", not "uploaded": the event records a fact about the patrulje,
// not the success of an HTTP request.
//
// # Several photographs per team, deliberately
//
// A team is photographed more than once — at the start, at the finish, twice
// because the first one was blurred — so this event is **not** "the team's current
// photo". Each photograph is its own event and its own row, keyed by content hash
// (see the projection's primary key). That is the difference from hej's portrait
// event, where a second capture replaces the first.
//
// Re-delivering the same photograph is therefore not "a replacement", it is the
// same fact stated twice: identical bytes hash to the same refs and the projection
// converges on the same row. This is what makes replaying the camera app's
// failed-webhook log safe.
//
// A deletion needs its own event (PatruljePhotoPurged) rather than a
// PatruljePhotographed with an empty Ref, or a replay could not tell "deleted"
// from "malformed message".
type PatruljePhotographed struct {
	// TeamID is the domain identifier, resolved from TeamNumber at ingest.
	TeamID string `json:"teamId"`

	// Year is the season, not the calendar year the shutter was pressed. It is
	// the year in the subject, and the year the patrulje projection keys on.
	Year string `json:"year"`

	// TeamNumber is what the photo crew typed. Kept for provenance rather than
	// for lookups: it is how a human finds this photograph in the camera app's
	// on-disk archive, and how a mis-typed number is diagnosed after the fact.
	TeamNumber string `json:"teamNumber,omitempty"`

	// Type is the camera app's category — "start", "finish", … Free text on
	// purpose: it arrives as a query parameter, so an enum here would reject a
	// photograph over a label.
	Type string `json:"type,omitempty"`

	// Attention is the flag the crew set to mark a photograph as needing a look
	// (the camera app's "XXX_" filename prefix). Carried, not acted upon —
	// deciding what it means is a consumer's business.
	Attention bool `json:"attention,omitempty"`

	// Ref is the content hash of the display image: a re-encode bounded to a
	// sensible edge, not the uploaded file. Untrusted on the way in — the
	// projection's handler enforces the hash shape before writing it.
	Ref         string `json:"ref"`
	ContentType string `json:"contentType"`
	Bytes       int    `json:"bytes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`

	// Renditions are the smaller cached versions.
	//
	// May be empty: a photograph recorded before a rendition set existed has
	// none, and so does a replayed event from that era. Consumers must degrade to
	// Ref rather than treat it as a broken record.
	Renditions []PhotoRendition `json:"renditions,omitempty"`

	// Original is the uploaded file, stored verbatim. Nil means no original was
	// kept. Never served; see PhotoOriginal.
	Original *PhotoOriginal `json:"original,omitempty"`

	// Source is where the bytes came from.
	Source *PhotoSource `json:"source,omitempty"`

	// CapturedAt is when the photograph was taken, in UTC, as reported by the
	// camera app — not when this service processed it.
	//
	// Deriving it from the message's delivery time would change on every replay,
	// and it is the clock any retention policy has to work from: "the photograph
	// does not outlive the event" needs a timestamp on the row.
	CapturedAt time.Time `json:"capturedAt"`
}

// PatruljePhotoPurged says that specific photographs have been deleted.
//
// A separate event rather than a PatruljePhotographed with an empty Ref: a replay
// must be able to tell "deleted" from "malformed message", and an empty-ref
// photograph is indistinguishable from the latter. It also means the log records
// *why* the photograph went, which is the question anyone auditing the deletion of
// a picture of a minor will actually ask.
//
// Refs, plural and required, because a team has many photographs. Purging by team
// alone would be a footgun: the caller who meant "this blurred one" would erase
// the season.
type PatruljePhotoPurged struct {
	TeamID string `json:"teamId"`
	Year   string `json:"year"`

	// Refs are the display refs of the photographs that were deleted. The
	// projection removes exactly these rows; the rendition and original objects
	// they name are the blob store's business.
	Refs []string `json:"refs"`

	// Reason is free text for the log, e.g. "retention". Deliberately not an
	// enum: this is a note to a human reading the stream a year later, and the
	// set of reasons is not worth pinning down before there is a second one.
	Reason string `json:"reason,omitempty"`

	PurgedAt time.Time `json:"purgedAt"`
}
