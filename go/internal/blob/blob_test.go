package blob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stores returns each implementation, so the contract is tested once rather than
// twice — the point of the interface is that callers cannot tell them apart, and a
// test that only ever sees one of them is not testing that.
func stores(t *testing.T) map[string]Store {
	t.Helper()
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return map[string]Store{
		"file":   fs,
		"memory": NewMemoryStore(),
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			// Deliberately not text: an original is a JPEG kept byte-identical to
			// the upload, so nothing here may assume printable bytes.
			want := []byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x10, 'E', 'x', 'i', 'f', 0x00, 0x00, 0xff, 0xd9}

			ref, err := s.Put(t.Context(), want)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if !ref.Valid() {
				t.Fatalf("Put returned an invalid ref: %q", ref)
			}
			if ref != ComputeRef(want) {
				t.Fatalf("ref is not the content hash: got %q", ref)
			}

			rc, err := s.Get(t.Context(), ref)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer func() { _ = rc.Close() }()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("want % x, got % x", want, got)
			}
		})
	}
}

// The property the whole design rests on: a re-delivered webhook, or a stream
// replay, re-stores the same bytes, so Put must be a no-op rather than a duplicate
// or an error.
func TestPutIsIdempotent(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			data := []byte("same bytes twice")

			first, err := s.Put(t.Context(), data)
			if err != nil {
				t.Fatalf("first Put: %v", err)
			}
			second, err := s.Put(t.Context(), data)
			if err != nil {
				t.Fatalf("second Put: %v", err)
			}
			if first != second {
				t.Fatalf("refs differ: %q vs %q", first, second)
			}

			if ms, ok := s.(*MemoryStore); ok && ms.Len() != 1 {
				t.Fatalf("want 1 stored object, got %d", ms.Len())
			}
		})
	}
}

// Idempotent is not enough on its own: a second Put must not *rewrite* the file.
// The store is the archive and is documented as write-once, and rewriting would
// mean a reader could momentarily see a half-written object under a ref that is
// already published on the stream. Backdating the mtime and checking it survives is
// the cheapest observable proof that the bytes were left alone.
func TestFileStorePutDoesNotRewriteExistingObject(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	data := []byte("already archived")
	ref, err := s.Put(t.Context(), data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	p, err := s.path(ref)
	if err != nil {
		t.Fatalf("path: %v", err)
	}

	backdated := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, backdated, backdated); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if _, err := s.Put(t.Context(), data); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(backdated) {
		t.Fatalf("object was rewritten: mtime moved from %v to %v", backdated, info.ModTime())
	}
}

// The sharded layout is part of the storage contract, not an implementation detail
// that may drift: an operator restoring a backup, or a future migration to object
// storage, needs the on-disk name to be predictable from the ref alone.
func TestFileStoreLayoutIsSharded(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	ref, err := s.Put(t.Context(), []byte("layout"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	want := filepath.Join(root, string(ref)[:2], string(ref))
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected object at %s: %v", want, err)
	}

	got, err := s.path(ref)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if got != want {
		t.Fatalf("path %q, want %q", got, want)
	}

	// And nothing outside that one bucket: a stray file at the root would mean the
	// fan-out is being bypassed somewhere.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() || entries[0].Name() != string(ref)[:2] {
		t.Fatalf("root should hold exactly the one bucket dir, got %v", entries)
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ref := ComputeRef([]byte("never stored"))
			if _, err := s.Get(t.Context(), ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
			ok, err := s.Exists(t.Context(), ref)
			if err != nil {
				t.Fatalf("Exists: %v", err)
			}
			if ok {
				t.Fatal("want Exists false for a missing object")
			}
		})
	}
}

func TestExistsAfterPut(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ref, err := s.Put(t.Context(), []byte("present"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			ok, err := s.Exists(t.Context(), ref)
			if err != nil {
				t.Fatalf("Exists: %v", err)
			}
			if !ok {
				t.Fatal("want Exists true just after Put")
			}
		})
	}
}

// A purge or retention job must be safely re-runnable, so deleting something that
// is not there is a success, not an error.
func TestDeleteIsIdempotent(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ref, err := s.Put(t.Context(), []byte("to be deleted"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			for i := range 2 {
				if err := s.Delete(t.Context(), ref); err != nil {
					t.Fatalf("Delete #%d: %v", i+1, err)
				}
			}
			if ok, _ := s.Exists(t.Context(), ref); ok {
				t.Fatal("object still present after delete")
			}
		})
	}
}

func TestDeleteAbsentIsNotAnError(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Delete(t.Context(), ComputeRef([]byte("never stored"))); err != nil {
				t.Fatalf("Delete of an absent object: %v", err)
			}
		})
	}
}

// Ref.Valid is the single place `../` is made impossible (PRD 001 §6 requires
// validation, not escaping), so the table is worth being exhaustive about.
func TestRefValid(t *testing.T) {
	bad := []Ref{
		"",
		"../../etc/passwd",
		"not-hex-but-the-right-length-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Ref(strings.Repeat("a", 63)),         // one short
		Ref(strings.Repeat("a", 65)),         // one long
		Ref(strings.Repeat("g", 64)),         // right length, not hex
		Ref("../" + strings.Repeat("a", 61)), // traversal padded to the right length
	}
	for _, ref := range bad {
		if ref.Valid() {
			t.Fatalf("ref %q should be invalid", string(ref))
		}
	}
	if got := ComputeRef(nil); !got.Valid() {
		t.Fatalf("ComputeRef output must be valid, got %q", got)
	}
}

// Uppercase hex is accepted by Valid, because hex.DecodeString accepts it and this
// mirrors hej's implementation. It is pinned here so the looseness is a recorded
// choice rather than an oversight: ComputeRef only ever emits lowercase, so an
// uppercase ref can only come from something re-typing a hash by hand, and on a
// case-insensitive filesystem it would resolve to the same object anyway. If a
// caller ever needs refs to be canonical strings — comparing them for equality
// across an event boundary, say — that belongs in a stricter parse step, not in a
// silent tightening of Valid.
func TestRefValidAcceptsUppercaseHex(t *testing.T) {
	upper := Ref(strings.ToUpper(string(ComputeRef([]byte("case")))))
	if !upper.Valid() {
		t.Fatalf("uppercase ref %q unexpectedly rejected", upper)
	}
}

// A Ref arrives from an event body or a URL path, so it is untrusted. Every method
// that takes one must reject it rather than sanitise it into something plausible —
// a sanitised traversal attempt becomes an ordinary miss and never reaches a log.
func TestInvalidRefIsRejectedByEveryMethod(t *testing.T) {
	bad := []Ref{
		"../../etc/passwd",
		Ref(strings.Repeat("z", 64)), // right length, not hex
		"",
	}
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			for _, ref := range bad {
				if _, err := s.Get(t.Context(), ref); err == nil {
					t.Fatalf("Get(%q): want an error", string(ref))
				} else if errors.Is(err, ErrNotFound) {
					t.Fatalf("Get(%q): want a rejection, not ErrNotFound", string(ref))
				}
				if _, err := s.Exists(t.Context(), ref); err == nil {
					t.Fatalf("Exists(%q): want an error", string(ref))
				}
				if err := s.Delete(t.Context(), ref); err == nil {
					t.Fatalf("Delete(%q): want an error", string(ref))
				}
			}
		})
	}
}

// Put derives its own ref, so it cannot be handed a bad one — but path() is the
// gate, and this pins that Put goes through it.
func TestFileStorePathRejectsInvalidRef(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if _, err := s.path("../../etc/passwd"); err == nil {
		t.Fatal("path: want an error for a traversal ref")
	}
	if _, err := s.path(Ref(strings.Repeat("q", 64))); err == nil {
		t.Fatal("path: want an error for a non-hex ref")
	}
	// And the ref Put would compute is accepted, so the gate is not simply closed.
	if _, err := s.path(ComputeRef([]byte("ok"))); err != nil {
		t.Fatalf("path: computed ref rejected: %v", err)
	}
}

// Originals keep their EXIF by decision, so this volume can hold GPS coordinates of
// where a child was photographed. Nothing else on the host has any business reading
// it.
func TestFileStorePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blobs")
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("want root mode 0700, got %o", perm)
	}

	ref, err := s.Put(t.Context(), []byte("bytes"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	p, err := s.path(ref)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat object: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want object mode 0600, got %o", perm)
	}

	// The bucket directory too: 0755 there would let anything list the refs, and
	// enumerable refs are all an attacker needs, since the file mode only stops the
	// read if the ref is unknown.
	bi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatalf("Stat bucket: %v", err)
	}
	if perm := bi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("want bucket mode 0700, got %o", perm)
	}
}

// The case a Docker volume actually produces: the root already exists, and it is
// world-readable. MkdirAll is a no-op on an existing directory, so without the
// explicit Chmod in NewFileStore the 0700 intent is silently lost — the mount is
// created by the runtime at 0755 long before this code runs.
func TestFileStoreTightensAnExistingWorldReadableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "preexisting")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("pre-create root: %v", err)
	}
	// Confirm the premise, so this test cannot pass for the wrong reason (a umask
	// could have produced 0700 here and made the assertion vacuous).
	if info, err := os.Stat(root); err != nil {
		t.Fatalf("Stat: %v", err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("setup failed: root is %o, wanted 0755", info.Mode().Perm())
	}

	if _, err := NewFileStore(root); err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("an existing root must be tightened to 0700, got %o", perm)
	}
}

func TestNewFileStoreRejectsEmptyRoot(t *testing.T) {
	if _, err := NewFileStore(""); err == nil {
		t.Fatal("want an error for an empty root")
	}
}

// No temp files should survive a successful Put: a leaked .tmp-* in a bucket would
// be an object nothing references, nothing cleans up, and — since it is not named
// by its hash — nothing can even identify.
func TestFileStoreLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if _, err := s.Put(t.Context(), []byte("bytes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
			t.Fatalf("leaked temp file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// Ingest runs on the synchronous callback path, so a client that has given up must
// not leave this store working on its behalf — least of all writing bytes nobody
// will ever be told about.
func TestContextCancellationIsHonoured(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ref, err := s.Put(t.Context(), []byte("stored before cancellation"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			if _, err := s.Put(ctx, []byte("must not be written")); !errors.Is(err, context.Canceled) {
				t.Fatalf("Put: want context.Canceled, got %v", err)
			}
			if _, err := s.Get(ctx, ref); !errors.Is(err, context.Canceled) {
				t.Fatalf("Get: want context.Canceled, got %v", err)
			}
			if _, err := s.Exists(ctx, ref); !errors.Is(err, context.Canceled) {
				t.Fatalf("Exists: want context.Canceled, got %v", err)
			}
			if err := s.Delete(ctx, ref); !errors.Is(err, context.Canceled) {
				t.Fatalf("Delete: want context.Canceled, got %v", err)
			}

			// The cancelled Put must have stored nothing, and the cancelled Delete
			// must not have removed what was there.
			if ok, err := s.Exists(t.Context(), ref); err != nil || !ok {
				t.Fatalf("pre-existing object disappeared: ok=%v err=%v", ok, err)
			}
			if ok, err := s.Exists(t.Context(), ComputeRef([]byte("must not be written"))); err != nil || ok {
				t.Fatalf("cancelled Put stored bytes: ok=%v err=%v", ok, err)
			}
		})
	}
}

// Two goroutines storing identical bytes is the ordinary case under retried
// webhooks, and the temp-file-plus-rename dance is what has to make it safe: both
// must succeed, and neither may see or leave a partial object.
func TestConcurrentPutSameBytes(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			data := []byte("contended bytes")
			var wg sync.WaitGroup
			refs := make([]Ref, 8)
			errs := make([]error, 8)

			for i := range refs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					refs[i], errs[i] = s.Put(context.Background(), data)
				}(i)
			}
			wg.Wait()

			for i, err := range errs {
				if err != nil {
					t.Fatalf("Put %d: %v", i, err)
				}
				if refs[i] != refs[0] {
					t.Fatalf("ref %d differs: %q vs %q", i, refs[i], refs[0])
				}
			}

			rc, err := s.Get(t.Context(), refs[0])
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer func() { _ = rc.Close() }()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != string(data) {
				t.Fatalf("want %q, got %q", data, got)
			}
		})
	}
}
