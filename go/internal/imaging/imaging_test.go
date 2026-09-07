package imaging_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"foto.nathejk.dk/internal/imaging"
)

// jpegWithOrientation builds a real JPEG carrying an EXIF orientation tag.
//
// Constructed byte by byte rather than committed as a fixture: the point of the parser is
// that it reads a *structure*, so the test has to be able to vary byte order, tag order
// and the value itself — which a binary fixture cannot.
func jpegWithOrientation(t testing.TB, img image.Image, orientation int, bigEndian bool) []byte {
	t.Helper()

	var body bytes.Buffer
	if err := jpeg.Encode(&body, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	encoded := body.Bytes()
	if encoded[0] != 0xFF || encoded[1] != 0xD8 {
		t.Fatal("encoder did not produce an SOI")
	}

	var order binary.ByteOrder = binary.LittleEndian
	tiff := []byte{'I', 'I'}
	if bigEndian {
		order = binary.BigEndian
		tiff = []byte{'M', 'M'}
	}

	put16 := func(dst []byte, v uint16) []byte {
		b := make([]byte, 2)
		order.PutUint16(b, v)
		return append(dst, b...)
	}
	put32 := func(dst []byte, v uint32) []byte {
		b := make([]byte, 4)
		order.PutUint32(b, v)
		return append(dst, b...)
	}

	tiff = put16(tiff, 42)
	tiff = put32(tiff, 8) // IFD0 starts right after the header
	tiff = put16(tiff, 1) // one entry
	tiff = put16(tiff, 0x0112)
	tiff = put16(tiff, 3) // SHORT
	tiff = put32(tiff, 1) // count
	tiff = put16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0) // remaining 2 bytes of the value field
	tiff = put32(tiff, 0)     // no next IFD

	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xFF, 0xE1}
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(payload)+2))
	segment = append(segment, length...)
	segment = append(segment, payload...)

	// SOI, then our APP1, then the rest of the encoder's output.
	out := append([]byte{0xFF, 0xD8}, segment...)
	return append(out, encoded[2:]...)
}

// gradient is asymmetric in both axes, so any rotation or mirror is detectable by
// sampling corners — a symmetric test image would pass with the transform inverted.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(255 * x / max(w-1, 1)), G: uint8(255 * y / max(h-1, 1)), B: 0, A: 255})
		}
	}
	return img
}

func encodeJPEG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestReadOrientationBothByteOrders(t *testing.T) {
	for _, bigEndian := range []bool{false, true} {
		for want := 1; want <= 8; want++ {
			raw := jpegWithOrientation(t, gradient(8, 8), want, bigEndian)
			if got := imaging.ReadOrientation(raw); got != want {
				t.Errorf("bigEndian=%v orientation = %d, want %d", bigEndian, got, want)
			}
		}
	}
}

// Anything unexpected must read as "upright" rather than failing: a malformed tag is no
// reason to refuse the crew's photo.
func TestReadOrientationDefaultsToUpright(t *testing.T) {
	plain := encodeJPEG(t, gradient(4, 4))

	cases := map[string][]byte{
		"no exif segment": plain,
		"not a jpeg":      []byte("PNG or whatever"),
		"empty":           nil,
		"truncated":       plain[:8],
		// A truncated EXIF payload: correct marker, nonsense inside.
		"broken exif": append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x0A}, []byte("Exif\x00\x00II")...),
		"bad value":   jpegWithOrientation(t, gradient(4, 4), 99, false),
	}
	for name, raw := range cases {
		if got := imaging.ReadOrientation(raw); got != 1 {
			t.Errorf("%s: orientation = %d, want 1", name, got)
		}
	}
}

// Orientation 6 is a photo taken with the phone rotated: stored landscape, meant to be
// displayed portrait. Every rendition must come out portrait — this is the case that
// produces sideways faces when it is missed, and kamera hands over the camera file
// untouched, so it is a path real crews hit.
func TestPrepareRotatesAccordingToExif(t *testing.T) {
	// Landscape source: 40 wide, 20 high.
	raw := jpegWithOrientation(t, gradient(40, 20), 6, false)

	out, err := imaging.Prepare(raw, 1024, []int{256}, 85)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if out.Display.Width != 20 || out.Display.Height != 40 {
		t.Errorf("display image is %dx%d, want it turned upright (20x40)", out.Display.Width, out.Display.Height)
	}
	// And the thumbnail must agree — every rendition comes from one correction, so a
	// disagreement would mean the orientation was applied twice or not at all on one path.
	thumb := out.Thumbs[0]
	if thumb.Width > thumb.Height {
		t.Errorf("thumbnail is %dx%d, want portrait like the display image", thumb.Width, thumb.Height)
	}
	if out.Orientation != 6 {
		t.Errorf("Orientation = %d, want 6 recorded for the event", out.Orientation)
	}
}

func TestPrepareLeavesUprightImagesAlone(t *testing.T) {
	raw := jpegWithOrientation(t, gradient(40, 20), 1, false)
	out, err := imaging.Prepare(raw, 1024, []int{256}, 85)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if out.Display.Width != 40 || out.Display.Height != 20 {
		t.Errorf("got %dx%d, want the original 40x20", out.Display.Width, out.Display.Height)
	}
}

// Every orientation must produce a decodable image of plausible dimensions. A cheap test,
// but it is the one that catches an index inversion in the transform for values nobody
// looks at by hand (5 and 7).
func TestPrepareHandlesAllOrientations(t *testing.T) {
	for orientation := 1; orientation <= 8; orientation++ {
		raw := jpegWithOrientation(t, gradient(30, 10), orientation, false)
		out, err := imaging.Prepare(raw, 1024, []int{256}, 85)
		if err != nil {
			t.Fatalf("orientation %d: %v", orientation, err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(out.Display.Bytes))
		if err != nil {
			t.Fatalf("orientation %d: rendition bytes not a JPEG: %v", orientation, err)
		}
		wantSwapped := orientation >= 5
		gotSwapped := cfg.Width < cfg.Height
		if wantSwapped != gotSwapped {
			t.Errorf("orientation %d: rendered %dx%d, swapped=%v want swapped=%v",
				orientation, cfg.Width, cfg.Height, gotSwapped, wantSwapped)
		}
	}
}

// The rendition set PRD 001 §6 asks for: a 2000px display image plus thumb1024 and
// thumb256. The display image's name is empty because the event carries its ref in `Ref`,
// not in `renditions`.
func TestPrepareProducesThePrdRenditionSet(t *testing.T) {
	raw := encodeJPEG(t, gradient(4000, 3000))

	out, err := imaging.Prepare(raw, 2000, []int{1024, 256}, 85)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if out.Display.Name != "" {
		t.Errorf("display name = %q, want empty", out.Display.Name)
	}
	if out.Display.Width != 2000 || out.Display.Height != 1500 {
		t.Errorf("display = %dx%d, want 2000x1500", out.Display.Width, out.Display.Height)
	}
	if out.Format != "jpeg" {
		t.Errorf("format = %q, want jpeg", out.Format)
	}

	wantNames := []string{"thumb1024", "thumb256"}
	wantEdges := []int{1024, 256}
	if len(out.Thumbs) != len(wantNames) {
		t.Fatalf("got %d thumbnails, want %d", len(out.Thumbs), len(wantNames))
	}
	sizes := map[int]bool{}
	for i, thumb := range out.Thumbs {
		// Order is the order requested — a consumer indexing by position must not have to
		// guess.
		if thumb.Name != wantNames[i] {
			t.Errorf("thumb %d named %q, want %q", i, thumb.Name, wantNames[i])
		}
		if thumb.Width != wantEdges[i] {
			t.Errorf("%s is %dpx wide, want %d", thumb.Name, thumb.Width, wantEdges[i])
		}
		if thumb.Height != wantEdges[i]*3/4 {
			t.Errorf("%s is %dpx high, want %d — aspect ratio not preserved",
				thumb.Name, thumb.Height, wantEdges[i]*3/4)
		}
		if len(thumb.Bytes) == 0 {
			t.Errorf("%s has no bytes", thumb.Name)
		}
		if sizes[len(thumb.Bytes)] {
			t.Errorf("%s has the same byte length as another rendition", thumb.Name)
		}
		sizes[len(thumb.Bytes)] = true
		if _, err := jpeg.Decode(bytes.NewReader(thumb.Bytes)); err != nil {
			t.Errorf("%s is not a decodable JPEG: %v", thumb.Name, err)
		}
	}

	// Smaller edge, smaller file — otherwise the size list buys nothing, and PRD 001's
	// "a grid must not download 800 full-size JPEGs" goal is unmet.
	if !(len(out.Display.Bytes) > len(out.Thumbs[0].Bytes) &&
		len(out.Thumbs[0].Bytes) > len(out.Thumbs[1].Bytes)) {
		t.Errorf("renditions do not shrink with their edge: %d, %d, %d",
			len(out.Display.Bytes), len(out.Thumbs[0].Bytes), len(out.Thumbs[1].Bytes))
	}
}

// Landscape, portrait and square all have to come out right. Getting the long edge wrong
// only shows on one of the three, which is why all three are here.
func TestPrepareDimensionsForEveryShape(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		w, h                   int
		wantW, wantH           int
		wantThumbW, wantThumbH int
	}{
		{"landscape", 2000, 1000, 1024, 512, 256, 128},
		{"portrait", 1000, 2000, 512, 1024, 128, 256},
		{"square", 2000, 2000, 1024, 1024, 256, 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := imaging.Prepare(encodeJPEG(t, gradient(tc.w, tc.h)), 1024, []int{256}, 85)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if out.Display.Width != tc.wantW || out.Display.Height != tc.wantH {
				t.Errorf("display = %dx%d, want %dx%d",
					out.Display.Width, out.Display.Height, tc.wantW, tc.wantH)
			}
			thumb := out.Thumbs[0]
			if thumb.Width != tc.wantThumbW || thumb.Height != tc.wantThumbH {
				t.Errorf("thumb = %dx%d, want %dx%d",
					thumb.Width, thumb.Height, tc.wantThumbW, tc.wantThumbH)
			}
		})
	}
}

// An upload smaller than the target is passed through at its own size rather than being
// blown up: a blurry "2000px" photo is worse than an honest 300px one, and it costs more
// to store and serve.
func TestPrepareNeverUpscales(t *testing.T) {
	out, err := imaging.Prepare(encodeJPEG(t, gradient(300, 200)), 2000, []int{1024, 256}, 85)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if out.Display.Width != 300 || out.Display.Height != 200 {
		t.Errorf("display = %dx%d, want the source 300x200", out.Display.Width, out.Display.Height)
	}
	// thumb1024 is above the source's edge too, so it is legitimately the same size as
	// the display image. A consumer must not be surprised by that.
	if out.Thumbs[0].Width != 300 || out.Thumbs[0].Height != 200 {
		t.Errorf("thumb1024 = %dx%d, want the source 300x200",
			out.Thumbs[0].Width, out.Thumbs[0].Height)
	}
	if out.Thumbs[1].Width != 256 {
		t.Errorf("thumb256 = %dpx wide, want 256 — it is below the source edge",
			out.Thumbs[1].Width)
	}
}

// No thumbnail sizes requested is a legitimate configuration, not an error.
func TestPrepareWithNoThumbnailSizes(t *testing.T) {
	out, err := imaging.Prepare(encodeJPEG(t, gradient(300, 300)), 1024, nil, 85)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(out.Thumbs) != 0 {
		t.Errorf("got %d thumbnails, want none", len(out.Thumbs))
	}
	if len(out.Display.Bytes) == 0 {
		t.Error("the display image must still be produced")
	}
}

// PNG and GIF in, JPEG out. kamera produces JPEG, but a replayed or hand-fed upload may
// not, and refusing it at ingest would lose a photograph over a container.
func TestPrepareAcceptsPngAndGif(t *testing.T) {
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, gradient(600, 300)); err != nil {
		t.Fatal(err)
	}
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, gradient(600, 300), nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		wantFormat string
		raw        []byte
	}{
		{"png", pngBuf.Bytes()},
		{"gif", gifBuf.Bytes()},
	} {
		t.Run(tc.wantFormat, func(t *testing.T) {
			out, err := imaging.Prepare(tc.raw, 256, []int{96}, 85)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if out.Format != tc.wantFormat {
				t.Errorf("format = %q, want %q", out.Format, tc.wantFormat)
			}
			// Format describes the bytes the caller stores as the original; renditions are
			// always JPEG regardless.
			if _, err := jpeg.Decode(bytes.NewReader(out.Display.Bytes)); err != nil {
				t.Errorf("display rendition is not a JPEG: %v", err)
			}
			if out.Display.Width != 256 || out.Display.Height != 128 {
				t.Errorf("display = %dx%d, want 256x128", out.Display.Width, out.Display.Height)
			}
			if len(out.Thumbs) != 1 || out.Thumbs[0].Name != "thumb96" {
				t.Errorf("thumbnails = %+v, want one named thumb96", out.Thumbs)
			}
			// GIF's non-zero-Min frames are the reason toRGBA re-anchors; a mistake there
			// shows as a blank or panicking rendition, not a wrong size.
			if _, err := jpeg.Decode(bytes.NewReader(out.Thumbs[0].Bytes)); err != nil {
				t.Errorf("thumbnail is not a JPEG: %v", err)
			}
		})
	}
}

// THE INVARIANT (package doc, PRD 001 §6): a rendition cannot carry metadata.
//
// foto stores the original with its EXIF — GPS included — intact, so "renditions are
// clean" is the only thing standing between a consumer and the coordinates of where a
// child was photographed. Asserted on the *output bytes* rather than on the code path,
// because the guarantee a reviewer needs is about what leaves the process.
func TestRenditionsCarryNoMetadata(t *testing.T) {
	// A JPEG with a real EXIF segment, plus recognisable payloads in the places metadata
	// hides: an APP1 (built by the helper) and bytes appended after EOI, which decoders
	// ignore — so a file can be a valid image and still smuggle something.
	raw := jpegWithOrientation(t, gradient(800, 600), 6, false)
	raw = append(raw, []byte("GPSLatitude-SecretMarker")...)

	// Sanity: the marker really is in the input, or this test proves nothing.
	if !bytes.Contains(raw, []byte("Exif")) || !bytes.Contains(raw, []byte("SecretMarker")) {
		t.Fatal("the test input does not contain the metadata it is meant to")
	}

	out, err := imaging.Prepare(raw, 400, []int{256, 96}, 85)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	all := append([]imaging.Rendition{out.Display}, out.Thumbs...)
	for _, r := range all {
		name := r.Name
		if name == "" {
			name = "display"
		}
		if bytes.Contains(r.Bytes, []byte("Exif")) {
			t.Errorf("%s contains an EXIF segment", name)
		}
		if bytes.Contains(r.Bytes, []byte("SecretMarker")) {
			t.Errorf("%s carried appended metadata through the re-encode", name)
		}
		// The orientation tag specifically: it is applied to the pixels, so a reader that
		// found one would rotate a second time.
		if got := imaging.ReadOrientation(r.Bytes); got != 1 {
			t.Errorf("%s declares orientation %d — a reader would rotate it twice", name, got)
		}
	}
}

func TestPrepareRejectsNonImages(t *testing.T) {
	for name, raw := range map[string][]byte{
		"garbage":  []byte("MZ\x90\x00 not an image"),
		"empty":    {},
		"nil":      nil,
		"html 404": []byte("<!doctype html><title>404 Not Found</title>"),
	} {
		if _, err := imaging.Prepare(raw, 1024, []int{256}, 85); !errors.Is(err, imaging.ErrNotAnImage) {
			t.Errorf("%s: err = %v, want ErrNotAnImage", name, err)
		}
	}
}

// hugePNG builds a structurally valid PNG header declaring w×h, with no pixel data.
//
// Only the IHDR has to be real: DecodeConfig stops there, which is the whole point of
// checking the header before allocating. The CRC is computed because Go's decoder verifies
// it and would otherwise reject the file as corrupt — making the test pass for the wrong
// reason.
func hugePNG(w, h uint32) []byte {
	ihdr := make([]byte, 0, 13)
	ihdr = binary.BigEndian.AppendUint32(ihdr, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0) // 8-bit RGBA, no interlace

	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)))
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

// A decompression bomb is a real input: `imageUrl` is attacker-influenceable, and a few
// hundred bytes of header can ask for hundreds of gigabytes of RGBA. It must be refused
// from the header, before anything is allocated — an OOM kill would take every in-flight
// photo with it, not just this one.
func TestPrepareRejectsAbsurdDimensions(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h uint32
	}{
		// Over MaxPixels while both edges look plausible.
		{"pixel count", 40000, 40000},
		// Under MaxPixels but with one absurd axis: the degenerate panorama that a pixel
		// count alone lets through.
		{"single edge", 100000, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := imaging.Prepare(hugePNG(tc.w, tc.h), 1024, []int{256}, 85)
			if !errors.Is(err, imaging.ErrTooLarge) {
				t.Fatalf("err = %v, want ErrTooLarge", err)
			}
			// Distinct from ErrNotAnImage: the bytes are a valid image, we are refusing to
			// expand them, and an operator wants those two logged differently.
			if errors.Is(err, imaging.ErrNotAnImage) {
				t.Error("an oversized image must not be reported as undecodable")
			}
		})
	}
}

// Just inside the limit must still work, or the guard is a size cap on real photographs.
// A 60 MP body is under MaxPixels and has to be accepted.
func TestPrepareAcceptsALargeButPlausiblePhoto(t *testing.T) {
	// 9504x6336 is a 2026 full-frame body: ~60 Mpx, under the 100 Mpx limit.
	if err := imagingBoundsAccept(t, 9504, 6336); err != nil {
		t.Errorf("a 60 MP photo was rejected: %v", err)
	}
}

// imagingBoundsAccept checks only that the header passes the bounds guard: it stops at the
// decode, because materialising 60 Mpx of test pixels would cost more than the assertion
// is worth.
func imagingBoundsAccept(t *testing.T, w, h uint32) error {
	t.Helper()
	_, err := imaging.Prepare(hugePNG(w, h), 1024, nil, 85)
	if errors.Is(err, imaging.ErrTooLarge) {
		return err
	}
	// Anything else — including ErrNotAnImage from the missing IDAT — means the bounds
	// check let it through, which is what this asserts.
	return nil
}

func TestThumbName(t *testing.T) {
	for edge, want := range map[int]string{256: "thumb256", 1024: "thumb1024", 96: "thumb96"} {
		if got := imaging.ThumbName(edge); got != want {
			t.Errorf("ThumbName(%d) = %q, want %q", edge, got, want)
		}
	}
}

func TestFitNeverUpscales(t *testing.T) {
	img := gradient(40, 30)
	got := imaging.Fit(img, 1024)
	if got.Bounds().Dx() != 40 || got.Bounds().Dy() != 30 {
		t.Errorf("got %v, want the original bounds", got.Bounds())
	}
}

// Area averaging is why this package does not use nearest-neighbour. On a source of
// alternating black and white columns, averaging must produce grey; nearest-neighbour
// would pick one column and produce pure black or white.
func TestFitAveragesRatherThanSamples(t *testing.T) {
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			shade := uint8(0)
			if x%2 == 0 {
				shade = 255
			}
			img.Set(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}

	small := imaging.Fit(img, size/8)
	r, _, _, _ := small.At(2, 2).RGBA()
	grey := r >> 8
	if grey < 100 || grey > 155 {
		t.Errorf("downscaled pixel = %d, want mid-grey — averaging is not happening", grey)
	}
}

// A gradient survives downscaling as a gradient: monotonically increasing left to right
// and top to bottom, with the corners near the source's corner values. This is the check
// that catches a transposed index or an off-by-one in the box bounds, which averaging
// alone would not reveal.
func TestFitPreservesAGradient(t *testing.T) {
	src := gradient(256, 256)
	small := imaging.Fit(src, 32)
	b := small.Bounds()
	if b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("got %v, want 32x32", b)
	}

	// R rises with x, G rises with y — so each channel checks one axis independently.
	for y := 0; y < 32; y++ {
		prev := -1
		for x := 0; x < 32; x++ {
			r, _, _, _ := small.At(x, y).RGBA()
			if v := int(r >> 8); v < prev {
				t.Fatalf("row %d: red fell from %d to %d at x=%d — x mapping is wrong", y, prev, v, x)
			} else {
				prev = v
			}
		}
	}
	for x := 0; x < 32; x++ {
		prev := -1
		for y := 0; y < 32; y++ {
			_, g, _, _ := small.At(x, y).RGBA()
			if v := int(g >> 8); v < prev {
				t.Fatalf("column %d: green fell from %d to %d at y=%d — y mapping is wrong", x, prev, v, y)
			} else {
				prev = v
			}
		}
	}

	// Corners: the top-left box averages the darkest source pixels, the bottom-right the
	// brightest. Tolerances are loose because a box filter averages a 8x8 source region.
	tlR, tlG, _, _ := small.At(0, 0).RGBA()
	brR, brG, _, _ := small.At(31, 31).RGBA()
	if tlR>>8 > 20 || tlG>>8 > 20 {
		t.Errorf("top-left = (%d,%d), want near black", tlR>>8, tlG>>8)
	}
	if brR>>8 < 235 || brG>>8 < 235 {
		t.Errorf("bottom-right = (%d,%d), want near white", brR>>8, brG>>8)
	}

	// Fully opaque in, fully opaque out — the alpha accumulator is easy to get wrong and
	// a half-transparent JPEG re-encode comes out muddy grey.
	if _, _, _, a := small.At(16, 16).RGBA(); a>>8 != 255 {
		t.Errorf("alpha = %d, want 255", a>>8)
	}
}
