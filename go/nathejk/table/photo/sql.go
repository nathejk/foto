package photo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SQL string handling.
//
// cqrs.Writer.Consume takes a finished statement, not a statement plus arguments,
// so this package must do its own quoting. That is the one genuinely dangerous
// thing in here, and it gets its own file for that reason.
//
// The values reaching these functions arrive in an event body from outside this
// process: a content hash, a category the camera app took from a query parameter,
// a URL. Refs are validated as hex before they get here, but Type, TeamNumber and
// sourceUrl are not constrained, so they are the injection surface.

// quote renders s as a single-quoted SQL string literal, escaping the characters
// MySQL and MariaDB treat specially.
//
// Deliberately not fmt.Sprintf("%q", s), which is what shared-go's entities use.
// %q is *Go* quoting: it emits double quotes and Go escape sequences, which
// happen to be accepted by MySQL in its default mode but are not the same
// language. Two concrete ways that bites — a backslash-terminated value, and
// ANSI_QUOTES mode where "..." becomes an identifier rather than a string — turn a
// projection statement into either a syntax error or a different statement than
// intended. Doing it properly costs a few lines.
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			b.WriteString(`\'`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case 0:
			// A NUL would truncate the statement at the driver or the server.
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 26:
			// Ctrl-Z terminates input on Windows clients; MySQL's own escaping
			// handles it, so this does too.
			b.WriteString(`\Z`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// datetime renders t as a SQL literal, or NULL when it is the zero time.
//
// NULL rather than a sentinel date, because "unknown when this was taken" is a
// real state — the camera app's payload could omit it — and any retention policy
// has to be able to distinguish it. A zero date formatted as 0000-00-00 is also
// rejected outright by MariaDB in strict mode.
func datetime(t time.Time) string {
	if t.IsZero() {
		return "NULL"
	}
	return quote(t.UTC().Format("2006-01-02 15:04:05"))
}

// encodeRenditions renders the rendition set for its column.
func encodeRenditions(renditions []PhotoRendition) (string, error) {
	if len(renditions) == 0 {
		// Empty string rather than "[]", so "no renditions" reads the same in SQL
		// as it does for a row written before the column existed.
		return "", nil
	}
	encoded, err := json.Marshal(renditions)
	if err != nil {
		return "", fmt.Errorf("photo: encode renditions: %w", err)
	}
	return string(encoded), nil
}

// decodeRenditions reads the column back.
//
// A malformed column yields no renditions rather than an error: the reader falls
// back to the display image, which is the same degradation a photograph recorded
// before renditions existed gets.
func decodeRenditions(encoded string) []PhotoRendition {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	var out []PhotoRendition
	if err := json.Unmarshal([]byte(encoded), &out); err != nil {
		return nil
	}
	return out
}
