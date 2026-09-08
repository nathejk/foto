package imaging_test

import (
	"bytes"
	"errors"
	"image/jpeg"
	"image/png"
	"testing"

	"foto.nathejk.dk/internal/imaging"
)

// Fuzzing, because ReadOrientation is the only place in the service that walks
// attacker-supplied binary structure by hand.
//
// Everything else about an upload goes through Go's own decoders, which are hardened and
// not ours to second-guess. ReadOrientation instead does its own segment arithmetic on
// bytes fetched from a URL in a request body — index arithmetic on untrusted lengths being
// the classic way to produce an out-of-range panic.
//
// A panic here would not take the API down (net/http recovers per connection and closes
// it), but the callback would then answer non-2xx, kamera would file the photo in
// webhook-failed.jsonl, and the crew would have an unexplained failed upload with only a
// stack trace in the log. Cheap to rule out; expensive to diagnose in the field.
//
// Run longer than the default when touching the parser:
//
//	go test ./internal/imaging -run=xxx -fuzz=FuzzReadOrientation -fuzztime=2m

func fuzzSeeds(f *testing.F) {
	f.Helper()

	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, gradient(24, 16), nil); err != nil {
		f.Fatal(err)
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, gradient(24, 16)); err != nil {
		f.Fatal(err)
	}

	f.Add(jpg.Bytes())
	f.Add(pngBuf.Bytes())
	f.Add(jpegWithOrientation(f, gradient(24, 16), 6, false))
	f.Add(jpegWithOrientation(f, gradient(24, 16), 3, true))
	// Structurally interesting nonsense: a bare SOI, a segment claiming a huge length,
	// and a PNG signature with no chunks.
	f.Add([]byte{0xFF, 0xD8})
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 'E', 'x', 'i', 'f', 0, 0})
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte{})
}

// FuzzReadOrientation: never panics, and never returns a value outside the EXIF range.
//
// The range matters as much as the absence of a panic: the value indexes a transform in
// applyOrientation *and* is published on the event, so an out-of-range answer would either
// rotate a photograph into nonsense or put a meaningless number in an append-only log.
func FuzzReadOrientation(f *testing.F) {
	fuzzSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		got := imaging.ReadOrientation(data)
		if got < 1 || got > 8 {
			t.Fatalf("orientation = %d, want 1-8", got)
		}
	})
}

// FuzzPrepare: the whole pipeline never panics, and whatever it accepts is internally
// consistent.
//
// Prepare is what the callback handler calls with bytes pulled from a remote URL, so the
// contract worth fuzzing is the one the handler relies on: either a typed error, or a
// complete set of renditions with positive dimensions that fit the requested edge. A
// success with a zero-sized rendition would be published as a photo nothing can display.
func FuzzPrepare(f *testing.F) {
	fuzzSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		const edge = 64
		out, err := imaging.Prepare(data, edge, []int{32, 8}, 85)
		if err != nil {
			if !errors.Is(err, imaging.ErrNotAnImage) && !errors.Is(err, imaging.ErrTooLarge) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			return
		}

		if len(out.Thumbs) != 2 {
			t.Fatalf("got %d thumbnails, want 2", len(out.Thumbs))
		}
		for _, r := range append([]imaging.Rendition{out.Display}, out.Thumbs...) {
			if r.Width < 1 || r.Height < 1 || len(r.Bytes) == 0 {
				t.Fatalf("rendition %q is empty: %dx%d, %d bytes", r.Name, r.Width, r.Height, len(r.Bytes))
			}
			if r.Width > edge && r.Height > edge {
				t.Fatalf("rendition %q is %dx%d, larger than the %dpx limit on both axes",
					r.Name, r.Width, r.Height, edge)
			}
		}
		if out.Orientation < 1 || out.Orientation > 8 {
			t.Fatalf("orientation = %d, want 1-8", out.Orientation)
		}
	})
}
