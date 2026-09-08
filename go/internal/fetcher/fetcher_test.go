package fetcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hostOf returns the hostname of a test server's URL, so the allowlist in a test
// is derived from the server rather than hardcoded to 127.0.0.1. httptest has
// switched loopback representation before now.
func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

// newFetcher builds a Fetcher whose client is the test server's own, which is
// what keeps these tests off the network entirely.
func newFetcher(t *testing.T, srv *httptest.Server, allowed []string, maxBytes int64) *Fetcher {
	t.Helper()
	f, err := New(allowed, maxBytes, 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv != nil {
		f.Client = srv.Client()
	}
	return f
}

func TestFetchSuccess(t *testing.T) {
	const want = "\xff\xd8\xff\xe0not-really-a-jpeg"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, want)
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 1<<20)

	data, ct, err := f.Fetch(context.Background(), srv.URL+"/photos/2026/start/Team-42_1.jpg")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(data) != want {
		t.Errorf("data = %q, want %q", data, want)
	}
	if ct != "image/jpeg" {
		t.Errorf("contentType = %q, want image/jpeg", ct)
	}
}

// The allowlist is only worth anything if it short-circuits before I/O, so this
// asserts the absence of a request rather than only the presence of an error.
func TestFetchDisallowedHostMakesNoRequest(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{"foto.nathejk.dk"}, 1<<20)

	_, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v, want ErrHostNotAllowed", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server was hit %d times, want 0", n)
	}
}

func TestFetchEmptyAllowlistDeniesEverything(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	for _, allowed := range [][]string{nil, {}, {""}, {"  "}} {
		f := newFetcher(t, srv, allowed, 1<<20)
		if _, _, err := f.Fetch(context.Background(), srv.URL); !errors.Is(err, ErrHostNotAllowed) {
			t.Errorf("allowlist %q: err = %v, want ErrHostNotAllowed", allowed, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("server was hit %d times, want 0", n)
	}
}

// Substring and suffix relationships are the classic allowlist bug: a naive
// strings.Contains or HasSuffix check accepts every one of these.
func TestFetchHostMatchIsExact(t *testing.T) {
	f := newFetcher(t, nil, []string{"foto.nathejk.dk"}, 1<<20)

	for _, raw := range []string{
		"https://evil-foto.nathejk.dk/photo.jpg",
		"https://foto.nathejk.dk.evil.example/photo.jpg",
		"https://nathejk.dk/photo.jpg",
		"https://xfoto.nathejk.dk/photo.jpg",
		"https://sub.foto.nathejk.dk/photo.jpg",
		// Userinfo makes the host look allowlisted to a human reading a log,
		// and to any check that inspects the raw string instead of Hostname().
		"https://foto.nathejk.dk@evil.example/photo.jpg",
	} {
		if _, _, err := f.Fetch(context.Background(), raw); !errors.Is(err, ErrHostNotAllowed) {
			t.Errorf("%s: err = %v, want ErrHostNotAllowed", raw, err)
		}
	}
}

// The mirror image of the test above: normalisation must not be so strict that
// equivalent spellings of the allowlisted name are refused.
func TestFetchHostMatchIgnoresCaseAndTrailingDot(t *testing.T) {
	f := newFetcher(t, nil, []string{"FOTO.Nathejk.DK."}, 1<<20)

	for _, raw := range []string{
		"https://foto.nathejk.dk/photo.jpg",
		"https://FOTO.NATHEJK.DK/photo.jpg",
		"https://foto.nathejk.dk.:8443/photo.jpg",
	} {
		// These are expected to get past the allowlist and then fail to
		// connect, since the name does not resolve to anything in the test
		// environment. Anything but ErrHostNotAllowed means the check passed.
		if _, _, err := f.Fetch(context.Background(), raw); errors.Is(err, ErrHostNotAllowed) {
			t.Errorf("%s: refused by allowlist, want accepted", raw)
		}
	}
}

func TestFetchRejectsNonHTTPSchemes(t *testing.T) {
	f := newFetcher(t, nil, []string{"foto.nathejk.dk", "localhost"}, 1<<20)

	for _, raw := range []string{
		"file:///etc/passwd",
		"file://foto.nathejk.dk/etc/passwd",
		"gopher://foto.nathejk.dk:70/_dangerous",
		"data:image/jpeg;base64,AAAA",
		"ftp://foto.nathejk.dk/photo.jpg",
		"//foto.nathejk.dk/photo.jpg",
		"/photos/2026/start/Team-42_1.jpg",
	} {
		_, _, err := f.Fetch(context.Background(), raw)
		if !errors.Is(err, ErrSchemeNotAllowed) && !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%s: err = %v, want ErrSchemeNotAllowed or ErrInvalidURL", raw, err)
		}
	}
}

func TestFetchRedirectToAllowedHostSucceeds(t *testing.T) {
	const want = "bytes-after-redirect"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, want)
	}))
	defer target.Close()

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/photo.png", http.StatusFound)
	}))
	defer front.Close()

	f := newFetcher(t, front, []string{hostOf(t, front.URL), hostOf(t, target.URL)}, 1<<20)

	data, ct, err := f.Fetch(context.Background(), front.URL+"/photo.png")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(data) != want {
		t.Errorf("data = %q, want %q", data, want)
	}
	if ct != "image/png" {
		t.Errorf("contentType = %q, want image/png", ct)
	}
}

// The attack this whole package exists for: an allowlisted host answers 302 to
// somewhere we would never have been allowed to ask for directly. The target is
// "localhost" on a port that genuinely serves bytes, so a missing per-hop check
// would make this test pass with data rather than merely fail to connect.
func TestFetchRedirectToDisallowedHostIsRefused(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		fmt.Fprint(w, "secret-metadata")
	}))
	defer target.Close()

	_, port, err := net.SplitHostPort(hostPort(t, target.URL))
	if err != nil {
		t.Fatalf("split target host: %v", err)
	}
	disallowed := "http://localhost:" + port + "/latest/meta-data/"

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, disallowed, http.StatusFound)
	}))
	defer front.Close()

	f := newFetcher(t, front, []string{hostOf(t, front.URL)}, 1<<20)

	if _, _, err := f.Fetch(context.Background(), front.URL+"/photo.jpg"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v, want ErrHostNotAllowed", err)
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target was hit %d times, want 0", n)
	}
}

func TestFetchRedirectLoopIsRefused(t *testing.T) {
	var hits atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 1<<20)
	f.MaxRedirects = 2

	if _, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg"); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
	// One initial request plus MaxRedirects followed hops, and no more: the cap
	// has to actually stop the chain, not just label its outcome.
	if n := hits.Load(); n != 3 {
		t.Errorf("server was hit %d times, want 3", n)
	}
}

func TestFetchRedirectsCanBeForbiddenEntirely(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, relativeRedirect(r), http.StatusFound)
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 1<<20)
	f.MaxRedirects = 0

	if _, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg"); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
}

func TestFetchBodyOverLimitIsRefused(t *testing.T) {
	body := strings.Repeat("A", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 512)

	if _, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// Exactly maxBytes must be accepted; the +1 read must not turn the boundary into
// an off-by-one that rejects a legal photograph.
func TestFetchBodyAtLimitIsAccepted(t *testing.T) {
	body := strings.Repeat("A", 512)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 512)

	data, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(data) != 512 {
		t.Errorf("len(data) = %d, want 512", len(data))
	}
}

// A server that declares a small Content-Length and then sends far more. The
// connection is hijacked because net/http refuses to write past a
// Content-Length it set itself — which is exactly why the header cannot be
// trusted to describe what a hostile peer will send.
//
// The invariant asserted is "never more than maxBytes reaches the caller", not
// ErrTooLarge: Go's transport stops the body at the declared length, so a lie in
// the *small* direction is self-limiting and cannot smuggle bytes past the cap.
// The dangerous direction is a body of unknown length, covered by
// TestFetchChunkedBodyOverLimitIsRefused.
func TestFetchLyingContentLengthCannotSmuggleBytes(t *testing.T) {
	const maxBytes = 512

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Read past the request head so the client is not blocked on write.
		br := bufio.NewReader(conn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: image/jpeg\r\nContent-Length: 12\r\n\r\n"))
		// Two orders of magnitude past both the declared length and the cap.
		for i := 0; i < 64; i++ {
			if _, err := conn.Write([]byte(strings.Repeat("A", 1024))); err != nil {
				return
			}
		}
	}()

	f := newFetcher(t, nil, []string{"127.0.0.1"}, maxBytes)
	// Keep-alives off, so the flood after the declared body is not left on an
	// idle pooled connection for a later request to trip over.
	f.Client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	data, _, err := f.Fetch(context.Background(), "http://"+ln.Addr().String()+"/photo.jpg")
	if err != nil && !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want nil or ErrTooLarge", err)
	}
	if int64(len(data)) > maxBytes {
		t.Errorf("len(data) = %d, want at most %d", len(data), maxBytes)
	}
	<-done
}

// The case Content-Length cannot describe: a chunked response of unknown length.
// Nothing but counting bytes as they arrive can stop this one.
func TestFetchChunkedBodyOverLimitIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flushing before the whole body is written forces chunked encoding, so
		// no Content-Length is sent at all.
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter is not a Flusher")
		}
		for i := 0; i < 64; i++ {
			if _, err := fmt.Fprint(w, strings.Repeat("A", 1024)); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer srv.Close()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 512)

	if _, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestFetchNon2xxReportsStatus(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusMovedPermanently + 100} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", code)
		}))

		f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 1<<20)
		_, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg")
		srv.Close()

		if !errors.Is(err, ErrUnexpectedStatus) {
			t.Fatalf("%d: err = %v, want ErrUnexpectedStatus", code, err)
		}
		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("%d: err = %v, want *StatusError", code, err)
		}
		if se.StatusCode != code {
			t.Errorf("StatusCode = %d, want %d", se.StatusCode, code)
		}
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprint(w, "too late")
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	f := newFetcher(t, srv, []string{hostOf(t, srv.URL)}, 1<<20)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, _, err := f.Fetch(ctx, srv.URL+"/photo.jpg")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	cancel()
}

// The fetcher's own timeout must fire even when the caller's context has none,
// so a kamera that accepts the connection and then says nothing cannot pin the
// callback open until kamera's own timeout gives up on us.
func TestFetchOwnTimeoutFires(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	f, err := New([]string{hostOf(t, srv.URL)}, 1<<20, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.Client = srv.Client()

	if _, _, err := f.Fetch(context.Background(), srv.URL+"/photo.jpg"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// A caller cannot re-open the redirect hole by handing us a permissive client:
// the policy is applied to a copy at Fetch time, not stored on the client.
func TestFetchIgnoresInjectedRedirectPolicy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secret-metadata")
	}))
	defer target.Close()

	_, port, err := net.SplitHostPort(hostPort(t, target.URL))
	if err != nil {
		t.Fatalf("split target host: %v", err)
	}

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+"/", http.StatusFound)
	}))
	defer front.Close()

	f := newFetcher(t, front, []string{hostOf(t, front.URL)}, 1<<20)
	f.Client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return nil }

	if _, _, err := f.Fetch(context.Background(), front.URL+"/photo.jpg"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v, want ErrHostNotAllowed", err)
	}
	if f.Client.CheckRedirect == nil {
		t.Error("Fetch cleared the caller's CheckRedirect; it must copy, not mutate")
	}
}

func TestNewRejectsUnboundedConfiguration(t *testing.T) {
	if _, err := New([]string{"foto.nathejk.dk"}, 0, time.Second); err == nil {
		t.Error("maxBytes 0 accepted, want error")
	}
	if _, err := New([]string{"foto.nathejk.dk"}, -1, time.Second); err == nil {
		t.Error("negative maxBytes accepted, want error")
	}
	if _, err := New([]string{"foto.nathejk.dk"}, 1, 0); err == nil {
		t.Error("timeout 0 accepted, want error")
	}
}

func TestFetchInvalidURL(t *testing.T) {
	f := newFetcher(t, nil, []string{"foto.nathejk.dk"}, 1<<20)

	if _, _, err := f.Fetch(context.Background(), "http://foto.nathejk.dk/\x7f\x00"); !errors.Is(err, ErrInvalidURL) {
		t.Errorf("err = %v, want ErrInvalidURL", err)
	}
	if _, _, err := f.Fetch(context.Background(), ""); !errors.Is(err, ErrSchemeNotAllowed) && !errors.Is(err, ErrInvalidURL) {
		t.Errorf("empty url: err = %v, want ErrInvalidURL or ErrSchemeNotAllowed", err)
	}
}

// hostPort returns the host:port of a URL, for building a same-port URL under a
// different hostname.
func hostPort(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}

// relativeRedirect echoes the request path back as a redirect target, keeping
// the redirect on the same (allowlisted) host so the hop cap is what refuses it.
func relativeRedirect(r *http.Request) string { return r.URL.Path + "/again" }
