package fetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIsBlockedIP is the SSRF IP-block matrix: every non-public address class
// must be rejected; ordinary public IPs allowed.
func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},             // loopback v4
		{"::1", true},                   // loopback v6
		{"10.0.0.5", true},              // private A
		{"172.16.0.1", true},            // private B
		{"192.168.1.1", true},           // private C
		{"169.254.169.254", true},       // link-local / cloud metadata
		{"fe80::1", true},               // link-local v6
		{"0.0.0.0", true},               // unspecified v4
		{"::", true},                    // unspecified v6
		{"224.0.0.1", true},             // multicast v4
		{"ff02::1", true},               // multicast v6
		{"100.64.0.1", true},            // CGNAT (RFC 6598)
		{"fc00::1", true},               // unique-local v6
		{"8.8.8.8", false},              // public
		{"1.1.1.1", false},              // public
		{"2606:4700:4700::1111", false}, // public v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isBlockedIP(ip); got != c.blocked {
			t.Errorf("isBlockedIP(%s)=%v want %v", c.ip, got, c.blocked)
		}
	}
}

func TestFetchRejectsNonHTTPScheme(t *testing.T) {
	f := New(Config{Timeout: time.Second, MaxBytes: 1 << 20, AllowedContentTypes: []string{"text/html"}})
	for _, u := range []string{"ftp://x/y", "file:///etc/passwd", "gopher://x", "data:text/html,hi"} {
		if _, _, err := f.Get(context.Background(), u); err == nil {
			t.Errorf("scheme %q should be rejected", u)
		}
	}
}

// TestFetchRejectsPrivateResolution uses an injected resolver that maps the
// host to a private IP — the fetch must refuse BEFORE dialing.
func TestFetchRejectsPrivateResolution(t *testing.T) {
	f := New(Config{
		Timeout: time.Second, MaxBytes: 1 << 20, AllowedContentTypes: []string{"text/html"},
		resolve: func(ctx context.Context, host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		},
	})
	if _, _, err := f.Get(context.Background(), "http://intranet.evil/"); err == nil {
		t.Fatal("private resolution must be rejected")
	} else if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err=%v want a 'blocked' SSRF error", err)
	}
}

// TestFetchAllowsPublicAndCapsBody starts a local server, but injects a resolver
// that returns the server's (loopback) IP AND a dialer override so the
// loopback-block does not trip — proving the happy path: status, MIME allowlist,
// body cap. We bypass isBlockedIP via the test-only allowLoopback flag.
func TestFetchAllowsPublicAndCapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.Repeat("A", 100)))
	}))
	defer srv.Close()

	f := New(Config{
		Timeout: 2 * time.Second, MaxBytes: 10, AllowedContentTypes: []string{"text/html"},
		allowLoopback: true, // test-only: permit the httptest loopback server
	})
	body, ct, err := f.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type=%q", ct)
	}
	if len(body) != 10 {
		t.Fatalf("body len=%d want 10 (MaxBytes cap)", len(body))
	}
}

func TestFetchRejectsDisallowedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("binary"))
	}))
	defer srv.Close()
	f := New(Config{Timeout: time.Second, MaxBytes: 1 << 20, AllowedContentTypes: []string{"text/html"}, allowLoopback: true})
	if _, _, err := f.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("disallowed content-type must be rejected")
	}
}
