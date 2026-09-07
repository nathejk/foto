package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// MemoryStore keeps objects in memory. For tests, and for running the API with no
// volume configured.
//
// It validates refs exactly as FileStore does, even though nothing in a map can be
// traversed out of. The point is that a test which exercises the Store contract
// against MemoryStore proves something about FileStore too: if the lenient
// implementation accepted "../../etc/passwd" as an ordinary miss, the shared tests
// could not assert rejection, and the only implementation where it matters would be
// the only one not covered by them.
type MemoryStore struct {
	mu      sync.RWMutex
	objects map[Ref][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: make(map[Ref][]byte)}
}

func (s *MemoryStore) Put(ctx context.Context, data []byte) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref := ComputeRef(data)

	// Copy: the caller may reuse or mutate its buffer — an ingest path reading an
	// HTTP body into a pooled slice is the obvious way for that to happen — and a
	// content-addressed store whose contents change out from under their own hash
	// is not one.
	stored := make([]byte, len(data))
	copy(stored, data)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[ref] = stored
	return ref, nil
}

func (s *MemoryStore) Get(ctx context.Context, ref Ref) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ref.Valid() {
		return nil, fmt.Errorf("blob: invalid ref %q", string(ref))
	}
	s.mu.RLock()
	data, ok := s.objects[ref]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *MemoryStore) Exists(ctx context.Context, ref Ref) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !ref.Valid() {
		return false, fmt.Errorf("blob: invalid ref %q", string(ref))
	}
	s.mu.RLock()
	_, ok := s.objects[ref]
	s.mu.RUnlock()
	return ok, nil
}

func (s *MemoryStore) Delete(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ref.Valid() {
		return fmt.Errorf("blob: invalid ref %q", string(ref))
	}
	s.mu.Lock()
	delete(s.objects, ref)
	s.mu.Unlock()
	return nil
}

// Len reports how many objects are stored. Test helper.
func (s *MemoryStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.objects)
}

var _ Store = (*MemoryStore)(nil)
