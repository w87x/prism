package netguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPrivateRanges(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.88.2", "172.16.0.9", "169.254.169.254", "100.64.1.1", "100.127.255.254", "0.0.0.0", "fe80::1", "fd00::1", "::ffff:10.0.0.1", "224.0.0.1"} {
		if !Private(net.ParseIP(ip)) {
			t.Errorf("%s should be private", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "2606:4700::1111"} {
		if Private(net.ParseIP(ip)) {
			t.Errorf("%s should be public", ip)
		}
	}
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"http://localhost:8080/x", "http://127.0.0.1/", "http://169.254.169.254/latest/meta-data", "http://[::1]/", "http://mac.local/", "http://printer.lan/", "http://100.100.1.1/", "http://foo.localhost/"} {
		if CheckURL(ctx, u) == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	if CheckURL(ctx, "http://8.8.8.8/") != nil {
		t.Error("a public IP literal must pass")
	}
	if CheckURL(ctx, "not a url") == nil {
		t.Error("garbage must be refused")
	}
}

// The guard is enforced where the connection is made, so it also stops a hostname that already
// resolved to something private (the DNS-rebinding case) and redirects into the local network.
func TestClientRefusesLoopbackAndRedirects(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer site.Close()
	if _, err := Client(false, 5*time.Second).Get(site.URL); err == nil {
		t.Fatal("dialing a loopback server must be refused")
	}
	resp, err := Client(true, 5*time.Second).Get(site.URL)
	if err != nil {
		t.Fatalf("allowPrivate must permit it: %v", err)
	}
	resp.Body.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, site.URL, http.StatusFound) }))
	defer redir.Close()
	if _, err := Client(false, 5*time.Second).Get(redir.URL); err == nil {
		t.Fatal("a redirect into loopback must be refused")
	}
}
