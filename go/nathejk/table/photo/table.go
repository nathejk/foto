package photo

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/jrgensen/cqrs"
)

//go:embed table.sql
var tableSchema string

// Table is the photograph entity: a projection, a publisher, and reads over the
// result.
//
// It is one type rather than three because the event shape is the thing all three
// have to agree about, and keeping them together is what stops a publisher writing
// a field the projection does not read.
type Table struct {
	p cqrs.Publisher
	w cqrs.Writer
	r cqrs.Reader
}

// New creates the schema and returns the entity.
//
// It returns an error rather than calling log.Fatalf the way shared-go's entities
// currently do. A library that terminates the process denies its caller the chance
// to report anything useful, and this one runs in a service whose whole job is to
// answer a webhook truthfully — "I could not create my schema" is exactly the
// answer the camera app needs so the photograph lands in its retry log.
//
// The publisher may be nil: the service starts before the broker is reachable, and
// projections must be constructible in that state. Publishing with a nil publisher
// returns ErrNoPublisher rather than panicking, so the ingest path can refuse the
// photograph instead of losing it.
func New(p cqrs.Publisher, w cqrs.Writer, r cqrs.Reader) (*Table, error) {
	if w == nil {
		return nil, fmt.Errorf("photo: a Writer is required")
	}
	if err := w.Consume(tableSchema); err != nil {
		return nil, fmt.Errorf("photo: create table: %w", err)
	}
	return &Table{p: p, w: w, r: r}, nil
}

// CreateTableSql exposes the schema, matching the convention shared-go's entities
// use.
func (t *Table) CreateTableSql() string { return tableSchema }

// Consumes declares the subjects this projection folds in.
//
// Both are this service's own events. That is the point of a projection here
// rather than a table written inline at ingest: the read model is derived from the
// log, so it can be dropped and rebuilt, and a photograph recorded by an older
// version of this code still lands correctly after a replay.
func (t *Table) Consumes() []cqrs.Subject {
	return []cqrs.Subject{
		consumePattern(photographedVerb),
		consumePattern(purgedVerb),
	}
}

// HandleMessage folds one event into the read model.
//
// Idempotent by construction, which the cqrs.Consumer contract requires: the
// insert is an upsert keyed by content hash, and the delete is unconditional.
func (t *Table) HandleMessage(msg cqrs.Message) error {
	switch {
	case msg.Subject().Match(matchPattern(photographedVerb)):
		return t.handlePhotographed(msg)
	case msg.Subject().Match(matchPattern(purgedVerb)):
		return t.handlePurged(msg)
	default:
		// Not an error: the mux may hand over a subject this projection does not
		// care about, and failing would dead-letter somebody else's event.
		return nil
	}
}

func (t *Table) handlePhotographed(msg cqrs.Message) error {
	var body PatruljePhotographed
	if err := msg.Body(&body); err != nil {
		return err
	}

	// The subject is the fallback for both identifiers, and the more trustworthy
	// source: the broker matched on it.
	teamID := body.TeamID
	if teamID == "" {
		teamID = subjectTeamID(msg.Subject())
	}
	year := body.Year
	if year == "" {
		year = subjectYear(msg.Subject())
	}
	if teamID == "" {
		return fmt.Errorf("photo: photographed with no teamId")
	}
	if year == "" {
		return fmt.Errorf("photo: photographed with no year")
	}

	// Failing rather than writing it. A bad ref would put a value in the row that
	// no object can satisfy, and every later read would degrade to "no photo"
	// while the row insisted there was one. Dead-lettering keeps the disagreement
	// visible instead of burying it in a column.
	if !validPhotoRef(body.Ref) {
		return fmt.Errorf("photo: ref %q is not a content hash", body.Ref)
	}

	renditions := body.validRenditions()
	encoded, err := encodeRenditions(renditions)
	if err != nil {
		return err
	}

	thumbRef := ""
	if len(renditions) > 0 {
		thumbRef = smallestRendition(renditions).Ref
	}

	// A malformed original ref costs the original, not the photograph — the same
	// rule as a rendition, and for the same reason: the consequence is "no future
	// re-render for this photo", not "no photo".
	var original PhotoOriginal
	if body.Original != nil && validPhotoRef(body.Original.Ref) {
		original = *body.Original
	}

	var source PhotoSource
	if body.Source != nil {
		source = *body.Source
	}

	// ON DUPLICATE KEY UPDATE, not INSERT IGNORE: a replay must be able to correct
	// a row written by an older version of this handler. Ignoring would make the
	// read model permanently reflect whichever code first saw the event.
	//
	// Written as one statement because cqrs.Writer.Consume takes exactly one.
	sql := fmt.Sprintf(
		"INSERT INTO photo SET year=%s, teamId=%s, ref=%s, type=%s, teamNumber=%s, "+
			"attention=%d, contentType=%s, bytes=%d, width=%d, height=%d, "+
			"renditions=%s, thumbRef=%s, "+
			"originalRef=%s, originalContentType=%s, originalBytes=%d, "+
			"originalWidth=%d, originalHeight=%d, orientation=%d, "+
			"sourceUrl=%s, sourceKind=%s, capturedAt=%s "+
			"ON DUPLICATE KEY UPDATE teamNumber=VALUES(teamNumber), "+
			"attention=VALUES(attention), contentType=VALUES(contentType), "+
			"bytes=VALUES(bytes), width=VALUES(width), height=VALUES(height), "+
			"renditions=VALUES(renditions), thumbRef=VALUES(thumbRef), "+
			"originalRef=VALUES(originalRef), "+
			"originalContentType=VALUES(originalContentType), "+
			"originalBytes=VALUES(originalBytes), originalWidth=VALUES(originalWidth), "+
			"originalHeight=VALUES(originalHeight), orientation=VALUES(orientation), "+
			"sourceUrl=VALUES(sourceUrl), sourceKind=VALUES(sourceKind), "+
			"capturedAt=VALUES(capturedAt)",
		quote(year), quote(teamID), quote(body.Ref), quote(body.Type), quote(body.TeamNumber),
		boolToInt(body.Attention), quote(body.ContentType), body.Bytes, body.Width, body.Height,
		quote(encoded), quote(thumbRef),
		quote(original.Ref), quote(original.ContentType), original.Bytes,
		original.Width, original.Height, original.Orientation,
		quote(truncate(source.URL, 999)), quote(source.Kind), datetime(body.CapturedAt),
	)

	return t.w.Consume(sql)
}

func (t *Table) handlePurged(msg cqrs.Message) error {
	var body PatruljePhotoPurged
	if err := msg.Body(&body); err != nil {
		return err
	}

	teamID := body.TeamID
	if teamID == "" {
		teamID = subjectTeamID(msg.Subject())
	}
	year := body.Year
	if year == "" {
		year = subjectYear(msg.Subject())
	}
	if teamID == "" || year == "" {
		return fmt.Errorf("photo: purged with no teamId or year")
	}

	// Only well-formed refs. A malformed one cannot match a row anyway, and
	// dropping it here keeps it out of the statement.
	refs := make([]string, 0, len(body.Refs))
	for _, ref := range body.Refs {
		if validPhotoRef(ref) {
			refs = append(refs, quote(ref))
		}
	}
	if len(refs) == 0 {
		// No refs is a no-op, not an error, and specifically NOT "delete the
		// team's photographs". A purge that silently widened to the whole team
		// because its ref list failed to decode is the worst outcome available
		// here, so the empty case does nothing at all.
		return nil
	}

	// Unconditional on anything but the refs: if one of these photographs is
	// re-photographed later, its own event comes later in the stream and reinserts
	// the row. Comparing timestamps here would make the outcome depend on replay
	// timing rather than on stream order.
	return t.w.Consume(fmt.Sprintf(
		"DELETE FROM photo WHERE year=%s AND teamId=%s AND ref IN (%s)",
		quote(year), quote(teamID), strings.Join(refs, ","),
	))
}

// validRenditions drops renditions whose ref is unusable and names the unnamed.
//
// A malformed rendition costs that one size, not the photograph: readers fall back
// to the display image, whereas failing the event would lose the photograph over a
// secondary artefact.
func (p PatruljePhotographed) validRenditions() []PhotoRendition {
	out := make([]PhotoRendition, 0, len(p.Renditions))
	for _, r := range p.Renditions {
		if !validPhotoRef(r.Ref) {
			continue
		}
		if r.Name == "" {
			// A rendition nothing can ask for by name is still worth keeping,
			// because a purge has to know its ref exists. Named by its own size so
			// it is at least addressable.
			r.Name = fmt.Sprintf("thumb%d", maxInt(r.Width, r.Height))
		}
		out = append(out, r)
	}
	return out
}

// smallestRendition returns the rendition with the smallest longest edge.
//
// "Smallest" rather than "first": this is the default served where a thumbnail is
// wanted, and the cheapest one is the right default. A rendition with unknown
// dimensions sorts last rather than winning by comparing zero.
func smallestRendition(renditions []PhotoRendition) PhotoRendition {
	best := renditions[0]
	bestEdge := maxInt(best.Width, best.Height)
	for _, r := range renditions[1:] {
		edge := maxInt(r.Width, r.Height)
		if bestEdge == 0 || (edge > 0 && edge < bestEdge) {
			best, bestEdge = r, edge
		}
	}
	return best
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
