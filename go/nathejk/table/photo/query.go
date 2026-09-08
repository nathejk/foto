package photo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound means no photograph matches.
var ErrNotFound = errors.New("photo: not found")

// Photo is a photograph as the read model holds it.
type Photo struct {
	Year        string           `json:"year"`
	TeamID      string           `json:"teamId"`
	TeamNumber  string           `json:"teamNumber,omitempty"`
	Type        string           `json:"type,omitempty"`
	Attention   bool             `json:"attention,omitempty"`
	Ref         string           `json:"ref"`
	ContentType string           `json:"contentType"`
	Bytes       int              `json:"bytes"`
	Width       int              `json:"width"`
	Height      int              `json:"height"`
	ThumbRef    string           `json:"thumbRef,omitempty"`
	Renditions  []PhotoRendition `json:"renditions,omitempty"`
	CapturedAt  *time.Time       `json:"capturedAt,omitempty"`

	// The original is deliberately NOT in this struct.
	//
	// It is in the table, because a rendition set has to be regenerable. It is
	// absent here because this type is what handlers serialise to callers, and the
	// original is the one object that must never be served: it carries the
	// upload's metadata, including whatever location the camera recorded. Leaving
	// the field out means no handler can leak it by forgetting to — which is a
	// stronger guarantee than remembering to strip it at each call site.
	//
	// A retention job needs those refs. It gets them from OriginalRefs below,
	// which is a separate, deliberately awkward call.
}

// queryTimeout bounds a read. Reads happen on the synchronous callback path.
const queryTimeout = 3 * time.Second

// ByTeam returns a team's photographs, newest first.
func (t *Table) ByTeam(ctx context.Context, year, teamID string) ([]Photo, error) {
	if t == nil || t.r == nil {
		return nil, errors.New("photo: no reader configured")
	}
	if year == "" || teamID == "" {
		return nil, ErrNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// Parameterised, unlike the projection's statements: reads go through
	// cqrs.Reader, which does take arguments. The string building in table.go is
	// forced by the Writer contract, not a house style to copy here.
	const query = `SELECT year, teamId, teamNumber, type, attention, ref, contentType,
			bytes, width, height, thumbRef, renditions, capturedAt
		FROM photo
		WHERE year = ? AND teamId = ?
		ORDER BY capturedAt IS NULL, capturedAt DESC, ref`

	rows, err := t.r.QueryContext(ctx, query, year, teamID)
	if err != nil {
		return nil, fmt.Errorf("photo: query by team: %w", err)
	}
	defer func() { _ = rows.Close() }()

	photos := []Photo{}
	for rows.Next() {
		var (
			p          Photo
			renditions string
			capturedAt sql.NullTime
		)
		if err := rows.Scan(
			&p.Year, &p.TeamID, &p.TeamNumber, &p.Type, &p.Attention, &p.Ref,
			&p.ContentType, &p.Bytes, &p.Width, &p.Height, &p.ThumbRef,
			&renditions, &capturedAt,
		); err != nil {
			return nil, fmt.Errorf("photo: scan: %w", err)
		}
		p.Renditions = decodeRenditions(renditions)
		if capturedAt.Valid {
			at := capturedAt.Time.UTC()
			p.CapturedAt = &at
		}
		photos = append(photos, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("photo: rows: %w", err)
	}
	return photos, nil
}

// Servable reports whether ref is an object this service may serve, and returns
// its content type.
//
// This is the access rule for the byte-serving route, and it is a query rather
// than a validation helper on purpose: whether a hash may be served is a fact
// about the read model, not about the string. A ref is servable if it is some
// photograph's display image or one of its renditions — and specifically NOT if it
// is only an original, because originals carry the upload's metadata.
//
// Consequence worth stating: an unknown hash is refused even though the blob store
// might hold it. Serving anything the store happens to contain would make the
// originals reachable by anyone who learned a hash.
func (t *Table) Servable(ctx context.Context, ref string) (contentType string, err error) {
	if t == nil || t.r == nil {
		return "", errors.New("photo: no reader configured")
	}
	if !validPhotoRef(ref) {
		return "", ErrNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// The display image is a direct hit. Renditions need the JSON column, so the
	// candidate rows are narrowed by thumbRef first and the set is checked in Go —
	// searching JSON in SQL would tie this to a MariaDB version for no benefit at
	// this scale.
	const displayQuery = `SELECT contentType FROM photo WHERE ref = ? LIMIT 1`
	err = t.r.QueryRowContext(ctx, displayQuery, ref).Scan(&contentType)
	switch {
	case err == nil:
		return contentType, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("photo: servable lookup: %w", err)
	}

	const renditionQuery = `SELECT renditions FROM photo WHERE thumbRef = ? OR renditions LIKE ? LIMIT 20`
	rows, err := t.r.QueryContext(ctx, renditionQuery, ref, "%"+ref+"%")
	if err != nil {
		return "", fmt.Errorf("photo: servable lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return "", fmt.Errorf("photo: scan: %w", err)
		}
		// The LIKE narrowed the rows; this is what actually decides. A substring
		// match on JSON must never be the authority — it would match a ref
		// appearing anywhere in the blob, including inside another field.
		for _, r := range decodeRenditions(encoded) {
			if r.Ref == ref {
				return r.ContentType, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("photo: rows: %w", err)
	}
	return "", ErrNotFound
}

// OriginalRefs returns the stored originals for photographs captured before a
// cutoff, for a retention job to delete.
//
// Separate from Photo, and returning bare refs rather than a struct a handler
// could serialise, so that reaching an original takes an explicit call that reads
// like what it is.
func (t *Table) OriginalRefs(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if t == nil || t.r == nil {
		return nil, errors.New("photo: no reader configured")
	}
	if limit <= 0 {
		limit = 100
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	const query = `SELECT originalRef FROM photo
		WHERE originalRef != '' AND capturedAt IS NOT NULL AND capturedAt < ?
		ORDER BY capturedAt LIMIT ?`

	rows, err := t.r.QueryContext(ctx, query, before.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("photo: original refs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("photo: scan: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("photo: rows: %w", err)
	}
	return refs, nil
}
