// Package imaging decodes an ingested photograph and derives the downscaled renditions
// foto serves (PRD 001 §6).
//
// # The invariant: originals carry metadata, renditions cannot
//
// This is the single most important property in the package, and it is the reason the
// package looks the way it does.
//
// foto stores the uploaded file **exactly as received** — byte-identical, every
// EXIF/IPTC/XMP/ICC block intact (PRD 001 §6, decided 2026-09-07). The archive is the
// canonical copy of the photograph, and `kamera` writes IPTC credit and copyright tags
// that would be wrong to discard. Nothing in this package touches those bytes: the caller
// stores the raw upload itself, and this package is never handed the job of giving it
// back. There is deliberately no metadata scrubber here — hej has one
// (`StripMetadata`), foto explicitly does not, and adding one would contradict the PRD.
//
// The consequence is that **the stored original may contain the GPS coordinates of where
// a child was photographed**, so the privacy boundary moves from "remember to strip" to
// "originals are never served". Everything this package returns is re-encoded from
// decoded pixels, which is not a filter over the container but a fresh JPEG built from an
// in-memory pixel buffer: there is no code path by which an input byte could reach a
// rendition. A consumer therefore cannot receive EXIF, and cannot receive it *by
// construction* rather than by a step somebody might forget.
//
// That is why orientation is read and returned. Re-encoding drops the tag along with
// everything else, so the rotation has to be applied to the renditions here (or faces
// come out sideways — the norm on iOS, which stores landscape pixels plus a tag), and the
// value has to be recorded on the event so a consumer can reason about the photograph
// without parsing the original it is not allowed to fetch.
//
// # Why this is not a dependency
//
// Two small, well-understood operations are needed — read one EXIF tag, and downscale.
// Both are implemented here rather than pulling in `golang.org/x/image` plus an EXIF
// library, because what they must do is narrow and, crucially, *testable against
// constructed inputs*: an EXIF header can be built byte by byte in a test, and a
// resampler can be checked against a known gradient. If a future need arrives that is
// genuinely a library's job (colour management, HEIC), that is the moment to add one.
//
// # Why it is separate from the ingest handler
//
// Everything here is a pure function of bytes. That is worth testing without a webhook, a
// blob store or a broker.
package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"

	// Decoders only. kamera produces JPEG, but a replayed or hand-fed upload may be PNG
	// or GIF; everything is re-encoded to JPEG.
	_ "image/gif"
	_ "image/png"
)

// ErrNotAnImage means the bytes could not be decoded as an image.
//
// It is the *only* content validation the ingest path needs: a decode either succeeds on
// real pixels or it does not, so no content-type header from `kamera` (or from whatever
// served `imageUrl`) has to be trusted, and no magic-byte table has to be maintained.
var ErrNotAnImage = errors.New("not a decodable image")

// ErrTooLarge means the image declares more pixels than this package will allocate for.
//
// Distinct from ErrNotAnImage on purpose: the bytes are a perfectly valid image, we are
// refusing to expand them. The ingest path should log the declared dimensions, because
// "the photo is bigger than the limit" and "the photo is corrupt" want different answers
// from an operator.
var ErrTooLarge = errors.New("image dimensions exceed the decode limit")

// MaxEdge and MaxPixels bound what Prepare will decode.
//
// # Why a limit exists at all
//
// The bytes arrive from a URL in a request body (PRD 001 §5). A decompression bomb is
// therefore a real input, not a hypothetical: a few hundred kilobytes of PNG can declare
// 60000×60000, and `image.Decode` will faithfully try to allocate it — here twice over,
// since Fit copies to RGBA. That is 4 bytes per pixel per buffer, so the process is OOM-
// killed before any of our code runs, taking every in-flight photo with it. Bounding the
// size is not about rejecting large photographs, it is about the failure being one 400
// instead of a restart.
//
// # Why the numbers
//
// MaxPixels is 100 megapixels: comfortably above any camera `kamera` runs on (a 2026
// phone is ~50 MP, a full-frame body ~60 MP) and about 400 MB of RGBA — large, but a
// bounded, survivable allocation. MaxEdge catches the degenerate panorama that stays
// under the pixel count while making one axis absurd (1×200000000 is 200 Mpx, but
// 40000×2500 is only 100 Mpx and still a 40000-wide RGBA row). Both are checked against
// `image.DecodeConfig`, i.e. against the *header*, before any pixel buffer is allocated —
// checking after the decode would be checking after the damage.
const (
	MaxEdge   = 30000
	MaxPixels = 100 * 1000 * 1000
)

// Rendition is one encoded image derived from an upload.
//
// Name is what the event and the projection key it by, and what a client asks for in
// `GET /photos/{ref}/{name}`. It is derived from the size (`thumb256`) rather than being
// a label like "small": a label needs a table somewhere to say what it means, and that
// table is what drifts from the pixels.
type Rendition struct {
	Name   string
	Bytes  []byte
	Width  int
	Height int
}

// Photo is a prepared photograph: the display image and every thumbnail asked for.
//
// Note what is *not* here: the original. foto stores the raw upload verbatim and does not
// route it through this package, so there is nothing for this type to hand back — see the
// package doc.
type Photo struct {
	// Display is the largest rendition, bounded by the caller's edge limit. Its Name is
	// empty: it is the photograph itself, not a variant of it, and the event carries its
	// ref in `Ref` rather than in `Renditions` (PRD 001 §8).
	Display Rendition

	// Thumbs are the smaller renditions, in the order requested.
	//
	// A slice rather than one thumbnail because more sizes are expected (a grid
	// thumbnail is a different size from a full-screen view), and retrofitting a list
	// onto a single field means changing an event shape that is already on an
	// append-only log. Generated from the same decode as Display, so no rendition can
	// disagree with another about orientation.
	Thumbs []Rendition

	// Orientation is the EXIF orientation the upload declared (1–8).
	//
	// Display and Thumbs have the rotation *applied*; the stored original still holds the
	// sensor's pixels and its own tag. Recording the value here anyway is what lets a
	// consumer reason about the photograph without parsing an original it is not allowed
	// to fetch, and makes the rotation we applied auditable from the log (PRD 001 §8).
	Orientation int

	// Format is the decoded format of the upload ("jpeg", "png", "gif"). It describes the
	// bytes the caller stores as the original; every rendition above is JPEG.
	Format string
}

// Dimensions reports the pixel extent an image's header declares, without decoding
// it.
//
// This is the *stored original's* size: the sensor's width and height, before any
// rotation is applied. For a photograph taken sideways they are therefore swapped
// relative to the renditions Prepare produces — which is exactly why the event
// records both these numbers and the orientation separately.
//
// It shares checkBounds with Prepare deliberately. The alternative was an
// image.DecodeConfig call at the ingest site, which would have meant two places
// deciding what "too large" means, free to drift apart until one accepted a
// photograph the other refused.
func Dimensions(raw []byte) (width, height int, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return 0, 0, ErrNotAnImage
	}
	if err := checkBounds(cfg.Width, cfg.Height); err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// Prepare decodes raw, corrects its orientation, and encodes the display image plus one
// thumbnail per entry in thumbEdges.
//
// edge bounds the longest side of the display image; each thumbEdge does the same for one
// thumbnail. Nothing is ever upscaled: a small upload stays small rather than being blown
// up into a blurry "large" image — which also means a thumbnail can legitimately come
// back the same size as the display image when the upload is tiny.
//
// One decode feeds every rendition. That is deliberate and load-bearing: decoding per
// size would let two renditions of the same photograph disagree about orientation, and
// the bug would only show on the sizes nobody looks at closely.
func Prepare(raw []byte, edge int, thumbEdges []int, quality int) (Photo, error) {
	// Header first. The point is to refuse an absurd allocation *before* making it, so
	// this must run before image.Decode and not after it.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return Photo{}, ErrNotAnImage
	}
	if err := checkBounds(cfg.Width, cfg.Height); err != nil {
		return Photo{}, err
	}

	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Photo{}, ErrNotAnImage
	}

	orientation := 1
	if format == "jpeg" {
		// Only JPEG carries the tag. Read before anything else, because both the applied
		// rotation below and the recorded Orientation come from it.
		orientation = ReadOrientation(raw)
		img = applyOrientation(img, orientation)
	}

	out := Photo{Orientation: orientation, Format: format}

	out.Display, err = render(img, "", edge, quality)
	if err != nil {
		return Photo{}, err
	}

	out.Thumbs = make([]Rendition, 0, len(thumbEdges))
	for _, thumbEdge := range thumbEdges {
		thumb, terr := render(img, ThumbName(thumbEdge), thumbEdge, quality)
		if terr != nil {
			return Photo{}, terr
		}
		out.Thumbs = append(out.Thumbs, thumb)
	}

	return out, nil
}

// checkBounds rejects dimensions this package will not allocate for. See MaxPixels.
//
// The multiplication is done in int64 because that is the whole hazard: on a 32-bit build
// `cfg.Width * cfg.Height` for a bomb overflows into a small positive number and the
// guard waves it through — the check itself becoming the vulnerability.
func checkBounds(w, h int) error {
	if w <= 0 || h <= 0 {
		// A header claiming zero or negative extent is not something to downscale. Decode
		// would fail on most such files anyway, but not reliably enough to depend on.
		return ErrNotAnImage
	}
	if w > MaxEdge || h > MaxEdge {
		return fmt.Errorf("%w: %dx%d has an edge over %d", ErrTooLarge, w, h, MaxEdge)
	}
	if int64(w)*int64(h) > int64(MaxPixels) {
		return fmt.Errorf("%w: %dx%d is over %d pixels", ErrTooLarge, w, h, MaxPixels)
	}
	return nil
}

// ThumbName is the canonical name for a thumbnail of the given longest edge.
//
// One function so the producer and every consumer agree without a shared constant per
// size — adding a size should not require editing a name table. The display image is not
// named by this: its name is empty, because the event addresses it as the photograph
// itself (see Photo.Display).
func ThumbName(edge int) string {
	return fmt.Sprintf("thumb%d", edge)
}

func render(img image.Image, name string, edge, quality int) (Rendition, error) {
	scaled := Fit(img, edge)
	encoded, err := encode(scaled, quality)
	if err != nil {
		return Rendition{}, err
	}
	b := scaled.Bounds()
	return Rendition{Name: name, Bytes: encoded, Width: b.Dx(), Height: b.Dy()}, nil
}

func encode(img image.Image, quality int) ([]byte, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Fit scales img down so its longest edge is at most edge, preserving aspect ratio.
// Images already within the limit are returned untouched.
//
// # Why area averaging rather than nearest-neighbour
//
// The grid thumbnail is roughly an eighth of the source's edge, and nearest-neighbour at
// that ratio throws away 63 of every 64 pixels: it aliases badly, and on a group of
// scouts it eats exactly the fine detail — faces, badge numbers — that makes the
// thumbnail worth having. Averaging the source pixels that cover each destination pixel
// is the correct filter for minification, it is a dozen lines, and it needs no dependency.
//
// Note this is a *box* filter, not Lanczos. For pure minification the difference is
// slight; for enlargement it would matter, and this function never enlarges.
func Fit(img image.Image, edge int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return img
	}
	if w <= edge && h <= edge {
		return img
	}

	newW, newH := w, h
	if w >= h {
		newW = edge
		newH = h * edge / w
	} else {
		newH = edge
		newW = w * edge / h
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	// Work through an RGBA copy so pixel access is direct rather than going through the
	// generic At() of whatever concrete type decoded: this loop touches every source
	// pixel exactly once, and At() on a YCbCr image — which is what every JPEG decodes
	// to — converts colour space per call.
	src := toRGBA(img)
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))

	for y := 0; y < newH; y++ {
		// The source rows covered by this destination row.
		y0 := y * h / newH
		y1 := (y + 1) * h / newH
		if y1 <= y0 {
			y1 = y0 + 1
		}

		for x := 0; x < newW; x++ {
			x0 := x * w / newW
			x1 := (x + 1) * w / newW
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				offset := src.PixOffset(x0, sy)
				for sx := x0; sx < x1; sx++ {
					p := src.Pix[offset : offset+4]
					r += uint64(p[0])
					g += uint64(p[1])
					b += uint64(p[2])
					a += uint64(p[3])
					n++
					offset += 4
				}
			}
			if n == 0 {
				continue
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o+0] = mean(r, n)
			dst.Pix[o+1] = mean(g, n)
			dst.Pix[o+2] = mean(b, n)
			dst.Pix[o+3] = mean(a, n)
		}
	}
	return dst
}

// mean returns sum/n as a byte, clamped.
//
// # Why uint64 and why the clamp
//
// gosec flags the obvious `uint8(r / n)` on a uint32 accumulator (G115), and it is right
// — not for the reason the arithmetic suggests, but for a reachable one. Each channel
// sums bytes, so `sum/n ≤ 255` always; the hazard is `sum` itself overflowing. With
// uint32 that happens once a single destination pixel averages more than ~16.8 M source
// pixels (255 × n > 2³²), which silently produces a wrapped, wrong colour rather than an
// error.
//
// Today's call sites cannot reach it — the display edge is in the thousands, so a box is
// at most (w×h)/edge² pixels — but that is an argument from the callers, not from the
// function, and it would stop being true the first time someone called `Fit(img, 8)` for
// an icon. uint64 removes the class outright at no measurable cost.
//
// The clamp is then unreachable by construction, and kept anyway: it makes the narrowing
// conversion provably safe to a reader (and to the linter) instead of requiring them to
// redo the algebra above.
func mean(sum, n uint64) uint8 {
	v := sum / n
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

// toRGBA returns img as an *image.RGBA anchored at (0,0).
//
// The re-anchoring matters: a decoded image can have a non-zero Min (a GIF frame
// routinely does), and every pixel index in this package assumes zero-based bounds.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba
	}
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(out, out.Bounds(), img, bounds.Min, draw.Src)
	return out
}

// ReadOrientation returns the EXIF orientation of a JPEG (1–8), or 1 when there is no
// tag, the file is not a JPEG, or anything about the structure is unexpected.
//
// # Why foto reads this at all
//
// Because renditions are re-encoded from pixels, which *drops* the tag. A phone that
// stores a photo rotated and describes the rotation in EXIF — the norm on iOS, and
// `kamera` hands over the camera file untouched — would otherwise have its photograph
// served sideways. The value is also recorded on the event, so a consumer never has to
// open the original to know which way up the photograph goes.
//
// Every failure returns 1 rather than an error: a missing or malformed tag is not a
// reason to refuse the crew's photo, it just means "assume upright".
func ReadOrientation(raw []byte) int {
	const upright = 1

	exif := findExifSegment(raw)
	if len(exif) < 8 {
		return upright
	}

	// TIFF header: byte order, magic 42, offset of the first IFD.
	var order binary.ByteOrder
	switch {
	case exif[0] == 'I' && exif[1] == 'I':
		order = binary.LittleEndian
	case exif[0] == 'M' && exif[1] == 'M':
		order = binary.BigEndian
	default:
		return upright
	}
	if order.Uint16(exif[2:4]) != 42 {
		return upright
	}

	ifd := int(order.Uint32(exif[4:8]))
	if ifd < 8 || ifd+2 > len(exif) {
		return upright
	}

	count := int(order.Uint16(exif[ifd : ifd+2]))
	entries := exif[ifd+2:]
	const entrySize = 12
	for i := 0; i < count; i++ {
		off := i * entrySize
		if off+entrySize > len(entries) {
			return upright
		}
		entry := entries[off : off+entrySize]
		if order.Uint16(entry[0:2]) != 0x0112 { // Orientation
			continue
		}
		// Type 3 is SHORT, and a SHORT value sits inline in the first two bytes of the
		// value field rather than at an offset.
		if order.Uint16(entry[2:4]) != 3 {
			return upright
		}
		value := int(order.Uint16(entry[8:10]))
		if value < 1 || value > 8 {
			return upright
		}
		return value
	}
	return upright
}

// findExifSegment returns the TIFF block inside the JPEG's APP1/Exif segment, or nil.
func findExifSegment(raw []byte) []byte {
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 { // SOI
		return nil
	}

	i := 2
	for i+4 <= len(raw) {
		if raw[i] != 0xFF {
			// Not at a marker, so the structure is not what we expect — and guessing a
			// way forward through arbitrary bytes is how a parser becomes a hazard.
			return nil
		}
		marker := raw[i+1]
		// Start of scan: image data begins, so there is no metadata left to find.
		if marker == 0xDA {
			return nil
		}
		// Standalone markers carry no length.
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9) {
			i += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(raw[i+2 : i+4]))
		if length < 2 || i+2+length > len(raw) {
			return nil
		}
		payload := raw[i+4 : i+2+length]
		if marker == 0xE1 && len(payload) >= 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
			return payload[6:]
		}
		i += 2 + length
	}
	return nil
}

// applyOrientation returns img transformed so it is upright.
//
// The eight EXIF values cover every combination of a quarter turn and a mirror. They are
// applied through one coordinate mapping rather than eight bespoke loops, because the
// bespoke version is where 6 and 8 get swapped — the classic "everyone's photos are
// upside down" bug.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}

	src := toRGBA(img)
	w, h := src.Rect.Dx(), src.Rect.Dy()

	// Values 5–8 involve a quarter turn, so the result's axes are swapped.
	outW, outH := w, h
	if orientation >= 5 {
		outW, outH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))

	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			var sx, sy int
			switch orientation {
			case 2: // mirrored horizontally
				sx, sy = w-1-x, y
			case 3: // rotated 180°
				sx, sy = w-1-x, h-1-y
			case 4: // mirrored vertically
				sx, sy = x, h-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // rotated 90° clockwise
				sx, sy = y, h-1-x
			case 7: // transversed
				sx, sy = w-1-y, h-1-x
			case 8: // rotated 90° counter-clockwise
				sx, sy = w-1-y, x
			default:
				sx, sy = x, y
			}
			so := src.PixOffset(sx, sy)
			do := dst.PixOffset(x, y)
			copy(dst.Pix[do:do+4], src.Pix[so:so+4])
		}
	}
	return dst
}
