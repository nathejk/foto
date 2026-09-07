package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileStore keeps objects on a filesystem, one file per object.
//
// This is the dev implementation and a viable production one on a mounted volume —
// the one volume `docker compose down -v` must not take with it. An S3-compatible
// Store can replace it without touching callers.
type FileStore struct {
	root string
}

// NewFileStore creates the root directory if needed and returns a store rooted
// there, and *enforces* mode 0o700 on it rather than only requesting it at creation.
//
// That distinction is load-bearing. When the root is a Docker volume or bind mount
// it already exists before this code runs, so MkdirAll is a no-op and leaves
// whatever mode the container runtime chose — in practice 0755, world-readable. The
// explicit Chmod is the only thing that tightens it, and there is a test asserting
// exactly that case.
//
// Why it matters more here than it might elsewhere: by explicit product decision
// (PRD 001 §6) `foto` stores originals byte-identical to the upload, with all
// EXIF/IPTC/XMP intact — no stripping, no re-encoding, because the archive is the
// canonical copy of the photograph and `kamera`'s IPTC credit and copyright tags
// would be wrong to discard. The consequence is that this directory can hold EXIF
// GPS coordinates of where an identifiable child was photographed. A world-readable
// directory on a shared volume would make that readable by anything on the host
// that can reach the volume, and the 0600 on each file would not save it, since the
// filenames are enumerable. So: 0700 on the directories, 0600 on the files.
//
// Failing rather than warning when the mode cannot be set is deliberate: if the
// directory cannot be made private, the right outcome is not to put children's
// geotagged photographs in it anyway.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, errors.New("blob: empty root directory")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("blob: create root: %w", err)
	}
	// #nosec G302 -- 0700 is correct for a directory, not a loosening of 0600.
	// gosec's rule is written for files, where the execute bit is meaningless; on a
	// directory that bit is what makes it traversable, so 0600 would leave a store
	// nothing could read from. Owner-only is the point, and 0700 is how a directory
	// expresses it.
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("blob: secure root %s: %w", root, err)
	}
	return &FileStore{root: root}, nil
}

// path maps a Ref to a file path, fanning out on the first two hex characters.
//
// The fan-out is not premature optimisation: every photo produces an original plus
// a rendition per size, so the object count is a multiple of the photo count, and a
// flat directory holding tens of thousands of entries is awkward to list and, on
// some filesystems, slow to look up. Two hex characters give 256 evenly filled
// buckets, which is plenty at this scale — the hash is uniform, so no rebalancing
// question ever arises.
//
// It returns an error for an invalid Ref rather than sanitising one, because every
// Ref here should have come from ComputeRef; anything else means a bug or an attempt
// at path traversal, and both deserve to fail loudly. Sanitising would turn a
// traversal attempt into a plausible-looking miss, which is the worse outcome: the
// attempt would never reach a log.
func (s *FileStore) path(ref Ref) (string, error) {
	if !ref.Valid() {
		return "", fmt.Errorf("blob: invalid ref %q", string(ref))
	}
	name := string(ref)
	return filepath.Join(s.root, name[:2], name), nil
}

func (s *FileStore) Put(ctx context.Context, data []byte) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	ref := ComputeRef(data)
	dst, err := s.path(ref)
	if err != nil {
		return "", err
	}

	// Already stored: identical contents by definition, so there is nothing to do.
	// This is what makes a re-delivered webhook and a stream replay cheap, and it
	// also means the archive is never rewritten in place — write-once, as promised.
	if _, err := os.Stat(dst); err == nil {
		return ref, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("blob: stat: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", fmt.Errorf("blob: create bucket: %w", err)
	}

	// Write to a temp file in the same directory, then rename. Rename within a
	// directory is atomic, so a reader can never observe a partially written
	// object — which for a content-addressed store is worse than a missing one,
	// since the ref would then name bytes that do not hash to it, and the truncated
	// file would be indistinguishable from a complete one on every later Exists.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("blob: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// No-op once the rename has succeeded.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("blob: write: %w", err)
	}
	// Chmod through the open file, not the path: os.CreateTemp opens at 0600 already,
	// but that depends on the umask being sane, and this is the one file mode nobody
	// should have to reason about.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("blob: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("blob: close temp: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("blob: rename: %w", err)
	}
	return ref, nil
}

func (s *FileStore) Get(ctx context.Context, ref Ref) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := s.path(ref)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- the path is not attacker-controlled by the time it gets here.
	// s.path rejects any ref that is not exactly 64 hex characters, so the filename
	// cannot contain a separator, a dot segment, or anything else that escapes the
	// root. That check is the mitigation gosec is asking for; it is one call above.
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob: open: %w", err)
	}
	return f, nil
}

func (s *FileStore) Exists(ctx context.Context, ref Ref) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	p, err := s.path(ref)
	if err != nil {
		return false, err
	}
	switch _, err := os.Stat(p); {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("blob: stat: %w", err)
	}
}

func (s *FileStore) Delete(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.path(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob: remove: %w", err)
	}
	return nil
}

var _ Store = (*FileStore)(nil)
