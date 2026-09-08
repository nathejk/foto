package main

// Storing a photograph's bytes.
//
// Four objects come out of one upload: the original exactly as fetched, the display
// image, and one object per thumbnail. All four go into the content-addressed store
// before anything is published, because an event naming bytes that do not exist
// cannot be repaired — the log is append-only and every consumer would see a
// permanently broken photograph.

import (
	"context"
	"fmt"

	"foto.nathejk.dk/internal/imaging"
	"foto.nathejk.dk/nathejk/table/photo"
)

// storedPhoto is the result of putting one upload's objects in the store, in the
// shape the event wants.
type storedPhoto struct {
	display    photo.PhotoRendition
	renditions []photo.PhotoRendition
	original   *photo.PhotoOriginal
}

// storePhoto writes the original and every rendition, and returns their refs.
//
// The original is stored **exactly as fetched**: same bytes, same format, metadata
// intact. That is a product decision (PRD 001 §6) and it has a consequence this
// function cannot enforce on its own — the stored original may carry the GPS
// coordinates of where a child was photographed. What keeps that contained is that
// nothing serves an original: the read model's Servable only ever admits the
// display image and the renditions, and this is the only place an original's ref is
// produced.
func (app *application) storePhoto(ctx context.Context, raw []byte, prepared imaging.Photo) (storedPhoto, error) {
	var out storedPhoto

	// The original first. If a later step fails, an unreferenced original is a
	// harmless orphan; the reverse — renditions stored, original lost — would mean
	// this photograph could never be re-rendered at a new size.
	originalRef, err := app.blobs.Put(ctx, raw)
	if err != nil {
		return out, fmt.Errorf("store original: %w", err)
	}

	// Dimensions from the header, not from the display rendition: these describe the
	// stored bytes before rotation, so for a sideways photograph they are swapped
	// relative to what a viewer sees. imaging.Dimensions rather than a second
	// DecodeConfig here, so the bound check and the reported size cannot drift apart.
	originalWidth, originalHeight, err := imaging.Dimensions(raw)
	if err != nil {
		return out, fmt.Errorf("measure original: %w", err)
	}

	out.original = &photo.PhotoOriginal{
		Ref: originalRef.String(),
		// The upload's own format, not image/jpeg: these bytes were not re-encoded.
		ContentType: contentTypeForFormat(prepared.Format),
		Bytes:       len(raw),
		Width:       originalWidth,
		Height:      originalHeight,
		Orientation: prepared.Orientation,
	}

	displayRef, err := app.blobs.Put(ctx, prepared.Display.Bytes)
	if err != nil {
		return out, fmt.Errorf("store display image: %w", err)
	}
	out.display = photo.PhotoRendition{
		// The display image's name is empty: the event carries its ref in `Ref`
		// rather than in the rendition list.
		Ref: displayRef.String(),
		// Always JPEG, whatever the upload was, because every rendition is
		// re-encoded.
		ContentType: "image/jpeg",
		Bytes:       len(prepared.Display.Bytes),
		Width:       prepared.Display.Width,
		Height:      prepared.Display.Height,
	}

	out.renditions = make([]photo.PhotoRendition, 0, len(prepared.Thumbs))
	for _, thumb := range prepared.Thumbs {
		if len(thumb.Bytes) == 0 {
			continue
		}
		ref, err := app.blobs.Put(ctx, thumb.Bytes)
		if err != nil {
			return out, fmt.Errorf("store rendition %q: %w", thumb.Name, err)
		}
		out.renditions = append(out.renditions, photo.PhotoRendition{
			Name:        thumb.Name,
			Ref:         ref.String(),
			ContentType: "image/jpeg",
			Bytes:       len(thumb.Bytes),
			Width:       thumb.Width,
			Height:      thumb.Height,
		})
	}

	return out, nil
}

// contentTypeForFormat maps a decoded format name to a media type.
//
// Driven by what the decoder actually recognised, never by the Content-Type the
// upstream server sent: that header is a claim by the machine we fetched from, and
// the whole point of decoding first is not to have to trust it.
func contentTypeForFormat(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	default:
		// Only reachable if imaging grows a decoder without updating this switch.
		// A generic type is honest: the bytes are stored and their real format is
		// recoverable from them, which is more than a wrong specific type would be.
		return "application/octet-stream"
	}
}
