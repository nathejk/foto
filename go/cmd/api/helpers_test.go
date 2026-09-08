package main

// Test helpers shared by the handler tests.

import (
	"encoding/json"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"foto.nathejk.dk/internal/blob"
	"foto.nathejk.dk/nathejk/table/photo"
)

// blobRef is a named conversion, so a test reads as "this string is a ref" rather
// than as a cast.
func blobRef(ref string) blob.Ref { return blob.Ref(ref) }

// sqlmockRows builds a single-column result set.
func sqlmockRows(column string, values ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{column})
	for _, v := range values {
		rows = rows.AddRow(v)
	}
	return rows
}

// photoRow renders events as rows in the shape photo.ByTeam scans.
//
// The column list is duplicated from that query on purpose: if somebody adds a column
// there and not here, this fails loudly rather than scanning silently into the wrong
// fields.
func photoRow(events ...photo.PatruljePhotographed) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"year", "teamId", "teamNumber", "type", "attention", "ref", "contentType",
		"bytes", "width", "height", "thumbRef", "renditions", "capturedAt",
	})
	for _, e := range events {
		renditions, err := json.Marshal(e.Renditions)
		if err != nil {
			// A test helper that cannot encode its own fixture is a bug in the test,
			// not a condition to handle.
			panic(err)
		}
		thumbRef := ""
		if len(e.Renditions) > 0 {
			thumbRef = e.Renditions[len(e.Renditions)-1].Ref
		}
		rows = rows.AddRow(
			e.Year, e.TeamID, e.TeamNumber, e.Type, e.Attention, e.Ref, e.ContentType,
			e.Bytes, e.Width, e.Height, thumbRef, string(renditions), e.CapturedAt,
		)
	}
	return rows
}
