// Package blob stores opaque binary objects addressed by the sha256 of their
// contents.
//
// # Why this package is the entire backup scope
//
// `foto` is the single entrypoint for photographs into the nathejk ecosystem, and
// the event it publishes — `NATHEJK.<year>.patrulje.<teamID>.photographed` —
// carries a *reference*, never image bytes (PRD 001 §6). Every projection in this
// service can be truncated and refilled from the stream; the objects in here
// cannot. That makes this package the only state that must be backed up, and the
// reason it is kept well away from the projection tables: a rebuild must never be
// able to do to a photograph what it routinely does to a table.
//
// # Why content addressing
//
// Ingest is a webhook, and webhooks get re-delivered. Because a Ref is the hash of
// the bytes, fetching and storing the same photograph twice yields the same Ref and
// one object, so a retry — or a full stream replay — converges without re-uploading
// anything and without orphaning what is already stored. Idempotence here is a
// property of the addressing scheme rather than a deduplication step somebody has
// to remember to write.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// ErrNotFound is returned by Get when no object has the given ref.
//
// Callers on a read path are expected to degrade rather than fail: a photo whose
// bytes have gone missing should render as "no photo", never as a broken page or a
// 500. The event that named it is still a true statement about the world.
var ErrNotFound = errors.New("blob not found")

// Ref identifies an object by the hash of its contents.
//
// Deliberately a distinct type rather than a string: a Ref travels through event
// bodies, projection rows and URL paths, and being able to confuse it with a
// filename, a team id or a URL segment is how a content-addressed store quietly
// stops being content-addressed.
type Ref string

// String returns the canonical textual form, which is what goes on the wire, into
// the `photo` projection and into `/photos/{ref}`.
func (r Ref) String() string { return string(r) }

// Valid reports whether r looks like a hash this package produced: exactly 64
// hex characters.
//
// Storage implementations must check this before touching the filesystem. A Ref
// reaches them from an event body or from a URL path — untrusted input either way —
// and "../../etc/passwd" is a Ref-shaped string. PRD 001 §6 is explicit that `../`
// must be impossible by validation rather than by escaping, and this is where that
// is decided.
func (r Ref) Valid() bool {
	if len(r) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(string(r))
	return err == nil
}

// ComputeRef returns the Ref for the given bytes.
func ComputeRef(data []byte) Ref {
	sum := sha256.Sum256(data)
	return Ref(hex.EncodeToString(sum[:]))
}

// Store is the storage seam.
//
// Kept thin on purpose: the production choice between object storage and a mounted
// volume is still open, and a narrow interface is what keeps that a wiring change
// rather than a rewrite. Anything richer — content type, dimensions, rendition
// names, who was photographed — belongs in the `photo` projection that references
// the object, not here. This layer knows about bytes and nothing else, which is
// also why an original and a derived rendition are stored through the same calls.
type Store interface {
	// Put stores data and returns its Ref. Idempotent: storing identical bytes
	// twice yields the same Ref and one object.
	Put(ctx context.Context, data []byte) (Ref, error)

	// Get returns a reader for the object, or ErrNotFound. The caller closes it.
	Get(ctx context.Context, ref Ref) (io.ReadCloser, error)

	// Exists reports whether the object is present, without transferring it.
	Exists(ctx context.Context, ref Ref) (bool, error)

	// Delete removes the object. Deleting something absent is not an error, so a
	// retention or purge job can be re-run safely.
	Delete(ctx context.Context, ref Ref) error
}
