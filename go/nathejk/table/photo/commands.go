package photo

import (
	"errors"
	"fmt"
	"time"

	"github.com/jrgensen/cqrs"
)

// ErrNoPublisher means the broker is not connected.
//
// Its own sentinel so the ingest path can map it to a retryable HTTP status: the
// camera app writes a failed webhook to its own log and the photograph can be
// replayed, so "come back later" is honest and lossless. Silently dropping the
// event would not be.
var ErrNoPublisher = errors.New("photo: no event publisher configured")

// Photographed is the command: record that a patrulje was photographed.
//
// The bytes must already be in the blob store before this is called. That ordering
// is not negotiable and belongs in the caller, but it is worth stating where the
// event is published: "event published, bytes missing" is unrecoverable — every
// consumer sees a photograph that cannot be fetched, forever — whereas "bytes
// stored, event not published" is a collectable orphan and a retry away from
// correct.
type Photographed struct {
	Year       string
	TeamID     string
	TeamNumber string
	Type       string
	Attention  bool

	// Display is the re-encoded image consumers are served.
	Display PhotoRendition

	// Renditions are the smaller cached sizes.
	Renditions []PhotoRendition

	// Original is the upload as stored, verbatim. Optional.
	Original *PhotoOriginal

	// Source is where the bytes came from. Optional.
	Source *PhotoSource

	// CapturedAt is when the shutter was pressed, per the camera app.
	CapturedAt time.Time
}

// Publish validates the command and appends the event.
//
// Validation here rather than only in the projection's handler because this is the
// last point at which a mistake is still cheap: once a malformed event is on an
// append-only log, every replay for the rest of the log's life has to cope with it.
// The handler validates too — it must, since it also sees events written by older
// code — but it can only skip or dead-letter, not prevent.
func (t *Table) Publish(cmd Photographed) error {
	if t == nil || t.p == nil {
		return ErrNoPublisher
	}

	subject, err := PhotographedSubject(cmd.Year, cmd.TeamID)
	if err != nil {
		return fmt.Errorf("photo: %w", err)
	}
	if !validPhotoRef(cmd.Display.Ref) {
		return fmt.Errorf("photo: display ref %q is not a content hash", cmd.Display.Ref)
	}

	body := PatruljePhotographed{
		TeamID:      cmd.TeamID,
		Year:        cmd.Year,
		TeamNumber:  cmd.TeamNumber,
		Type:        cmd.Type,
		Attention:   cmd.Attention,
		Ref:         cmd.Display.Ref,
		ContentType: cmd.Display.ContentType,
		Bytes:       cmd.Display.Bytes,
		Width:       cmd.Display.Width,
		Height:      cmd.Display.Height,
		Renditions:  cmd.Renditions,
		Original:    cmd.Original,
		Source:      cmd.Source,
		CapturedAt:  cmd.CapturedAt.UTC(),
	}

	return t.publish(subject, body)
}

// Purge records that specific photographs have been deleted.
//
// It does not delete anything itself: the objects are the blob store's business
// and the row is the projection's. This is the announcement, and the audit record
// of why it happened.
func (t *Table) Purge(year, teamID, reason string, refs []string) error {
	if t == nil || t.p == nil {
		return ErrNoPublisher
	}
	if len(refs) == 0 {
		// Refusing rather than publishing an empty purge. An empty ref list means
		// the caller does not know what it is deleting, and a future reader of the
		// log could reasonably interpret it as "everything".
		return fmt.Errorf("photo: purge needs at least one ref")
	}

	subject, err := PurgeSubject(year, teamID)
	if err != nil {
		return fmt.Errorf("photo: %w", err)
	}

	return t.publish(subject, PatruljePhotoPurged{
		TeamID:   teamID,
		Year:     year,
		Refs:     refs,
		Reason:   reason,
		PurgedAt: time.Now().UTC(),
	})
}

// publish is the three-step cqrs idiom: ask the publisher for a message bound to a
// subject, set its body, send it.
func (t *Table) publish(subject cqrs.Subject, body any) error {
	msg := t.p.MessageFunc()(subject)
	if msg == nil {
		// The publisher exists but has no transport behind it yet — the service starts
		// before the broker is reachable. Reported as the same error as a missing
		// publisher, because it means the same thing to the caller: the photograph
		// cannot be announced, so do not acknowledge it.
		return ErrNoPublisher
	}
	if err := msg.SetBody(body); err != nil {
		return fmt.Errorf("photo: set event body: %w", err)
	}
	return t.p.Publish(msg)
}
