package photo

import (
	"fmt"
	"strings"

	"github.com/jrgensen/cqrs"
)

// Subject tokens. Kept as constants because the projection's Consumes patterns
// and the publisher's subject builders must agree, and a typo in one of them is a
// projection that silently never sees an event.
const (
	streamDomain = "NATHEJK"
	entityToken  = "patrulje"

	photographedVerb = "photographed"
	purgedVerb       = "photopurged"
)

// PhotographedSubject builds the subject a photograph is published on:
//
//	NATHEJK.<year>.patrulje.<teamId>.photographed
//
// # Why on NATHEJK and not a sibling stream
//
// A photograph of a team is a small, low-frequency domain fact about that team,
// which is what the NATHEJK stream is for — and `NATHEJK.>` already claims the
// subject space, so no broker topology change is needed. A sibling stream is what
// you reach for when volume forces it; a few thousand photographs a season does
// not.
//
// It also slots into the vocabulary already in use for this entity —
// NATHEJK.*.patrulje.*.{signedup,updated,numberassigned,started} — rather than
// inventing a parallel one.
//
// Provenance is not lost by sharing the stream: every message this service
// publishes is stamped with producer "foto-api" by the metatagger.
//
// # Why per team
//
// So that `nats stream purge --subject 'NATHEJK.2026.patrulje.team-abc.>'` can
// erase one team's photographic history and nothing else. That is not
// hypothetical tidiness — it is the mechanism by which a request to remove a
// child's photographs is actually honoured.
//
// Several photographs share one subject, which is fine and was checked rather
// than assumed: the NATHEJK stream has max_msgs_per_subject = -1, so every
// message on a subject is retained. A stream configured to keep only the last
// message per subject would silently discard all but the most recent photograph.
func PhotographedSubject(year, teamID string) (cqrs.Subject, error) {
	return entitySubject(year, teamID, photographedVerb)
}

// PurgeSubject builds the subject a purge is published on:
//
//	NATHEJK.<year>.patrulje.<teamId>.photopurged
//
// The verb is "photopurged" rather than "purged" because this stream carries
// other facts about a patrulje, and a bare "purged" would read as though the team
// itself had been erased.
func PurgeSubject(year, teamID string) (cqrs.Subject, error) {
	return entitySubject(year, teamID, purgedVerb)
}

func entitySubject(year, teamID, verb string) (cqrs.Subject, error) {
	if err := validSubjectToken(year, "year"); err != nil {
		return nil, err
	}
	if err := validSubjectToken(teamID, "team id"); err != nil {
		return nil, err
	}
	return cqrs.SubjectFromStr(fmt.Sprintf("%s.%s.%s.%s.%s",
		streamDomain, year, entityToken, teamID, verb)), nil
}

// consumePattern builds a wildcard subject for Consumes.
//
// The ":" form is the convention in this codebase for a subscription pattern:
// stream.subject.FromStr replaces the first colon with a dot, so
// "NATHEJK:*.patrulje.*.photographed" and "NATHEJK.*.patrulje.*.photographed"
// parse identically. The colon is documentation — it marks where the stream name
// ends and the subject filter begins — and it matches how shared-go's entities
// declare theirs.
func consumePattern(verb string) cqrs.Subject {
	return cqrs.SubjectFromStr(fmt.Sprintf("%s:*.%s.*.%s", streamDomain, entityToken, verb))
}

// matchPattern is the dotted form, for Subject.Match inside HandleMessage.
func matchPattern(verb string) string {
	return fmt.Sprintf("%s.*.%s.*.%s", streamDomain, entityToken, verb)
}

// validSubjectToken rejects anything that would not survive as a single NATS
// subject token.
//
// Not cosmetic. An id containing a dot would split into extra tokens, still match
// `NATHEJK.>`, and publish successfully — while quietly no longer matching the
// per-team purge pattern. The photographs of exactly that team would then be the
// ones that could not be erased on request, and nothing would report it.
func validSubjectToken(s, what string) error {
	if s == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if strings.ContainsAny(s, ". \t\r\n*>") {
		return fmt.Errorf("%s %q is not a valid subject token", what, s)
	}
	return nil
}

// subjectYear and subjectTeamID read the tokens back out of a subject.
//
//	NATHEJK . <year> . patrulje . <teamId> . <verb>
//	   0         1         2          3         4
//
// These exist as a fallback for a message whose body omits a field. The subject is
// the more trustworthy of the two: the broker matched on it, so it cannot have
// been silently wrong in a way the body can.
func subjectYear(subj cqrs.Subject) string {
	return subjectPart(subj, 1)
}

func subjectTeamID(subj cqrs.Subject) string {
	return subjectPart(subj, 3)
}

func subjectPart(subj cqrs.Subject, i int) string {
	if subj == nil {
		return ""
	}
	parts := subj.Parts()
	if len(parts) <= i {
		return ""
	}
	return parts[i]
}

// validPhotoRef reports whether ref looks like a content hash from the blob store.
//
// Duplicates blob.Ref.Valid rather than calling it, because this package may not
// import internal/... (see the package doc). The check is 64 lowercase hex
// characters — a sha256 in hex — and it is here because a Ref arrives in an event
// body from outside this process, is interpolated into a SQL statement, and later
// appears in a URL path. "../../etc/passwd" is a Ref-shaped string.
//
// Stricter than blob.Ref.Valid on one point, deliberately: uppercase is rejected.
// The store produces lowercase, so an uppercase ref is not something we wrote, and
// two spellings of one hash would defeat the primary key that makes this
// projection idempotent.
func validPhotoRef(ref string) bool {
	if len(ref) != 64 {
		return false
	}
	for _, c := range ref {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
