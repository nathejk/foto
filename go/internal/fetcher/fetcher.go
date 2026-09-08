// Package fetcher performs the one server-side HTTP GET this service cannot
// avoid: pulling the image bytes for a URL that arrived in a request body.
//
// # Why a whole package for one GET
//
// kamera's webhook does not carry the photograph. It carries an `imageUrl`
// pointing back at kamera's own web server, and we decided not to modify the
// kamera repo, so foto dereferences that URL itself (PRD 001 §5 happy path
// step 2, §6 Non-Functional/Security). A URL taken from a request body and
// fetched by the server is the textbook SSRF sink: the value looks like data
// but is really an instruction to make a request from inside our network, where
// the interesting targets — the MySQL container, the NATS box, a cloud metadata
// endpoint on 169.254.169.254 — are reachable and unauthenticated in a way they
// never are from outside. `http.Get(payload.ImageUrl)` would be a working
// exploit primitive with a friendly name.
//
// The rejected alternative was to scatter these checks through the callback
// handler. They were kept together here because they only work as a set: an
// allowlist without redirect checking is bypassed by a 302, a size cap that
// trusts Content-Length is bypassed by lying about it, and a handler that grows
// a second call site will get one of them wrong. Concentrating them also makes
// the failure modes testable in isolation, which is the only way to know the
// checks still hold after somebody edits this file.
//
// # What this package does not do
//
// It does not resolve hostnames and check the resulting IPs against
// RFC1918/loopback/link-local ranges. That was considered and deliberately left
// out: doing it correctly requires pinning the dialled address to the address
// that was vetted (otherwise DNS rebinding re-opens the hole between the check
// and the dial), which means a custom DialContext and a control hook, and it
// buys nothing here because the allowlist is a list of *our own* public
// hostnames — a deployment that allowlists an internal name has already made
// the decision. If the allowlist ever grows a user-supplied entry, that
// omission becomes load-bearing and must be revisited.
package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrInvalidURL means rawURL could not be parsed, or parsed to something with
// no host at all (a bare path, say). Distinct from ErrHostNotAllowed because a
// caller mapping these to HTTP statuses wants 400 for both but the log line
// should not claim an allowlist decision that was never made.
var ErrInvalidURL = errors.New("fetcher: invalid url")

// ErrSchemeNotAllowed means the URL was not http or https.
//
// The allowlist of schemes is closed rather than a blocklist of known-bad ones.
// `file:///etc/passwd` and `gopher://` are the famous ones, but the set is
// open-ended — Go's transport can be extended with arbitrary schemes via
// RegisterProtocol, and a blocklist written today cannot know about them.
var ErrSchemeNotAllowed = errors.New("fetcher: scheme not allowed")

// ErrHostNotAllowed means the URL's hostname is not on the allowlist. It is
// also what an empty allowlist returns for every URL, which is the intended
// behaviour: a missing or mistyped configuration value must deny everything
// rather than turn the service into an open proxy. Failing closed is the whole
// point of the check, so the degenerate case must fail closed too.
var ErrHostNotAllowed = errors.New("fetcher: host not allowed")

// ErrTooManyRedirects means the chain exceeded MaxRedirects.
//
// A cap is needed even though every hop is allowlisted: two allowlisted URLs
// can point at each other, and an unbounded loop is a request that never
// returns while holding a connection and a goroutine on the synchronous
// callback path.
var ErrTooManyRedirects = errors.New("fetcher: too many redirects")

// ErrTooLarge means the response body exceeded maxBytes.
//
// Its own error rather than a generic read failure because the caller's answer
// differs: a too-large photo is a 413-shaped, non-retryable refusal, whereas a
// truncated read is worth retrying.
var ErrTooLarge = errors.New("fetcher: response too large")

// ErrUnexpectedStatus reports a non-2xx response. Match it with errors.Is; use
// errors.As with *StatusError to recover the code itself.
var ErrUnexpectedStatus = errors.New("fetcher: unexpected status")

// StatusError carries the status code of a non-2xx response.
//
// The code is carried rather than folded into a string because the callback
// handler's decision depends on it: kamera 404ing an imageUrl means the photo
// is gone and retrying is pointless, while a 503 from kamera is exactly the
// case PRD 001 §5 wants replayed out of webhook-failed.jsonl.
type StatusError struct {
	StatusCode int
	Status     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("fetcher: unexpected status %s", e.Status)
}

// Is reports ErrUnexpectedStatus so callers can classify without errors.As.
func (e *StatusError) Is(target error) bool { return target == ErrUnexpectedStatus }

// defaultMaxRedirects bounds the hop count. Three is generous for the only
// legitimate case we expect (http→https, or a path normalisation), and small
// enough that a chain of allowlisted hosts cannot be used to fan out requests.
const defaultMaxRedirects = 3

// Fetcher pulls bytes over HTTP under a fixed set of restrictions.
//
// Configuration is per-instance and passed in, with no package-level default
// client or allowlist. A global would be mutable from anywhere and, worse,
// would have a usable zero value — and the usable zero value of an allowlist is
// "allow everything".
type Fetcher struct {
	// allowedHosts holds normalised (lowercased, trailing-dot-stripped)
	// hostnames. A map rather than a slice with strings.Contains: substring
	// matching is precisely the bug that lets evil-foto.nathejk.dk through a
	// check for foto.nathejk.dk, and exact map lookup cannot express it.
	allowedHosts map[string]struct{}

	maxBytes int64
	timeout  time.Duration

	// MaxRedirects caps the redirect chain. Zero means no redirect may be
	// followed at all, which is a legitimate configuration, so the default is
	// applied in New rather than being inferred from the zero value here.
	MaxRedirects int

	// Client may be replaced after construction — that is how tests point the
	// fetcher at an httptest server. It is exported rather than injected
	// through New because the security-relevant policy does not live on it:
	// New rewrites CheckRedirect on a copy of whatever client is used at Fetch
	// time, so a caller cannot disable the per-hop allowlist check by supplying
	// a permissive client.
	Client *http.Client
}

// New returns a Fetcher restricted to allowedHosts, maxBytes and timeout.
//
// It errors on a non-positive maxBytes or timeout rather than substituting a
// default. "Unlimited" and "never times out" are the two values that must not
// be reachable by forgetting to set something, and a zero from an unset env var
// is exactly how they would be reached. An empty allowedHosts is *not* an
// error, deliberately: it is a valid, fully-closed configuration, and Fetch
// refuses every URL under it (see ErrHostNotAllowed).
func New(allowedHosts []string, maxBytes int64, timeout time.Duration) (*Fetcher, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("fetcher: maxBytes must be positive, got %d", maxBytes)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("fetcher: timeout must be positive, got %s", timeout)
	}

	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		// Blank entries are dropped rather than stored. A trailing comma in
		// FOTO_ALLOWED_HOSTS produces one, and a stored "" would match the
		// hostname of a URL like `http:///photo.jpg`, which parses with an
		// empty host.
		if n := normaliseHost(h); n != "" {
			allowed[n] = struct{}{}
		}
	}

	return &Fetcher{
		allowedHosts: allowed,
		maxBytes:     maxBytes,
		timeout:      timeout,
		MaxRedirects: defaultMaxRedirects,
		Client:       &http.Client{},
	}, nil
}

// normaliseHost lowercases a hostname and strips any trailing dot.
//
// The trailing dot matters: `foto.nathejk.dk.` is the fully-qualified form of
// the same name and resolves identically, so treating it as a different string
// would be a one-character allowlist bypass. Lowercasing is for the same class
// of trick with `FOTO.NATHEJK.DK`. Note this is a textual normalisation only —
// it does no punycode/IDNA folding, so a Unicode homograph of an allowlisted
// name will simply not match, which is the safe direction.
func normaliseHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// hostAllowed reports whether u's hostname is on the allowlist.
//
// url.Hostname() is used rather than u.Host because the latter includes the
// port, so a check against it would either fail for a legitimate
// `foto.nathejk.dk:8443` or, if written as a prefix comparison to cope, accept
// `foto.nathejk.dk.evil.example`. The port is intentionally not constrained:
// restricting it protects nothing an allowlisted host would not already expose.
func (f *Fetcher) hostAllowed(u *url.URL) bool {
	if len(f.allowedHosts) == 0 {
		return false
	}
	_, ok := f.allowedHosts[normaliseHost(u.Hostname())]
	return ok
}

// checkURL applies the scheme and host rules to a parsed URL. Every hop in a
// redirect chain goes through this, not just the first: the entire value of the
// allowlist is lost if an allowlisted host may 302 us to
// http://169.254.169.254/latest/meta-data/, which is the canonical cloud
// credential theft.
func (f *Fetcher) checkURL(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("%w: %q", ErrSchemeNotAllowed, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: no host in %q", ErrInvalidURL, u.Redacted())
	}
	if !f.hostAllowed(u) {
		return fmt.Errorf("%w: %q", ErrHostNotAllowed, u.Hostname())
	}
	return nil
}

// Fetch GETs rawURL and returns its body and the Content-Type header it came
// with.
//
// The returned contentType is untrusted metadata and must not be used to decide
// what the bytes are. It is whatever the remote server typed; a JPEG can be
// served as text/plain and an HTML page as image/png. The caller determines the
// real type by decoding the bytes (internal/imaging), and PRD 001 §5 requires an
// undecodable image to be rejected — that decision belongs to the decoder, not
// to this string. It is returned only because it is occasionally worth logging
// or echoing, never for dispatch.
//
// data is nil on every error return. A partially-read body is not a smaller
// photograph, and returning one would invite a caller to store a truncated
// original.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (data []byte, contentType string, err error) {
	if f == nil {
		return nil, "", errors.New("fetcher: nil fetcher")
	}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		// The parse error is not wrapped into the message: it can quote the
		// whole URL, and these URLs end up in logs. ErrInvalidURL plus the
		// caller's own context is enough to act on.
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	// Checked before any I/O, so a refused host costs no packet. That is not
	// only an optimisation: an unauthenticated caller who can make us connect
	// to an arbitrary host — even without seeing the response — has a port
	// scanner and a way to poke at things that act on a bare GET.
	if err := f.checkURL(u); err != nil {
		return nil, "", err
	}

	// Both a context deadline and the client's own bound would be redundant;
	// the context is the one that works, because it also stops the body read
	// after the headers have arrived — a server that dribbles one byte a second
	// is otherwise unbounded. The caller's ctx is still honoured: WithTimeout
	// only ever tightens it.
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	client := f.Client
	if client == nil {
		client = &http.Client{}
	}

	// A shallow copy, so the redirect policy below cannot be turned off by the
	// caller having set CheckRedirect on the client they injected, and so we do
	// not mutate a client that is shared with anything else. Transport,
	// CookieJar and Timeout are inherited as-is.
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > f.MaxRedirects {
			return fmt.Errorf("%w: %d", ErrTooManyRedirects, len(via))
		}
		// The redirect target gets the identical treatment as the original URL.
		// http.Client will not follow a scheme it cannot speak, but it is
		// checked anyway: relying on the transport's opinion of a scheme means
		// this check silently weakens if a protocol is ever registered.
		return f.checkURL(req.URL)
	}

	resp, err := c.Do(req)
	if err != nil {
		// c.Do wraps everything in *url.Error, which does implement Unwrap, so
		// the sentinels returned by CheckRedirect above survive errors.Is.
		// Returned unwrapped-but-wrapped rather than reduced to a generic
		// "fetch failed" precisely so ErrHostNotAllowed from hop three is still
		// distinguishable from a connection reset.
		return nil, "", fmt.Errorf("fetcher: get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body of an error response is not read at all. It is
		// attacker-chosen and could be gigabytes; nothing here needs it.
		return nil, "", &StatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}

	// maxBytes+1 is the whole trick: read one byte past the limit, and if that
	// byte exists the response was too large. Content-Length is deliberately
	// not consulted — it is a claim by the remote server, it is absent under
	// chunked encoding, and a hostile server declaring "Content-Length: 12"
	// before streaming 12GB is the exact bypass a Content-Length check has.
	// Enforcing it during the read means the cap holds regardless of what the
	// headers said. (http.MaxBytesReader was the alternative; it is documented
	// for server-side request bodies and reports its failure only as an opaque
	// error string, which would cost us ErrTooLarge as a distinct sentinel.)
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("fetcher: read body: %w", err)
	}
	if int64(len(body)) > f.maxBytes {
		return nil, "", fmt.Errorf("%w: over %d bytes", ErrTooLarge, f.maxBytes)
	}

	return body, resp.Header.Get("Content-Type"), nil
}
