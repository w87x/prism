package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/testutil"
	"prism/internal/tools"
)

// fakeProvider is an authorization server and a protected MCP endpoint in one, built to check what PRISM
// sends: the discovery documents, dynamic registration, PKCE, the resource parameter, and refresh handling.
type fakeProvider struct {
	*httptest.Server
	mu         sync.Mutex
	registered []map[string]any
	codes      map[string]codeInfo
	access     map[string]bool // valid access tokens
	refreshOK  map[string]bool // valid refresh tokens
	tokenSeq   int
	expiresIn  int
	grants     []string
	mcpAuth    []string // Authorization headers seen by the MCP endpoint
	noReg      bool
}

type codeInfo struct{ challenge, redirect, client, resource string }

func newProvider(t *testing.T) *fakeProvider {
	p := &fakeProvider{codes: map[string]codeInfo{}, access: map[string]bool{}, refreshOK: map[string]bool{}, expiresIn: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": p.URL + "/mcp", "authorization_servers": []string{p.URL}, "scopes_supported": []string{"tools.read"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{"authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token", "code_challenge_methods_supported": []string{"S256"}}
		if !p.noReg {
			meta["registration_endpoint"] = p.URL + "/register"
		}
		json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.registered = append(p.registered, body)
		p.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"client_id": "client-1"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.grants = append(p.grants, r.Form.Get("grant_type"))
		fail := func(msg string) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": msg})
		}
		issue := func() {
			p.tokenSeq++
			acc, ref := fmt.Sprintf("access-%d", p.tokenSeq), fmt.Sprintf("refresh-%d", p.tokenSeq)
			p.access[acc], p.refreshOK[ref] = true, true
			json.NewEncoder(w).Encode(map[string]any{"access_token": acc, "refresh_token": ref, "expires_in": p.expiresIn, "token_type": "Bearer"})
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			ci, ok := p.codes[r.Form.Get("code")]
			delete(p.codes, r.Form.Get("code"))
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			switch {
			case !ok:
				fail("unknown or reused code")
			case base64.RawURLEncoding.EncodeToString(sum[:]) != ci.challenge:
				fail("PKCE verifier does not match")
			case r.Form.Get("redirect_uri") != ci.redirect || r.Form.Get("client_id") != ci.client:
				fail("redirect_uri/client mismatch")
			case r.Form.Get("resource") != ci.resource:
				fail("resource mismatch")
			default:
				issue()
			}
		case "refresh_token":
			old := r.Form.Get("refresh_token")
			if !p.refreshOK[old] {
				fail("refresh token rejected")
				return
			}
			delete(p.refreshOK, old) // single use, like many providers
			issue()
		default:
			fail("unsupported grant")
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		p.mu.Lock()
		p.mcpAuth = append(p.mcpAuth, auth)
		ok := strings.HasPrefix(auth, "Bearer ") && p.access[strings.TrimPrefix(auth, "Bearer ")]
		p.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, p.URL))
			w.WriteHeader(401)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if len(req.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "secure"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "whoami", "description": "Who am I", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}}
		default:
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "signed-in user"}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
	})
	// "authorize": records what the client asked for and hands back a code, as the user clicking Allow would
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
			http.Error(w, "bad authorization request", 400)
			return
		}
		code := fmt.Sprintf("code-%d", time.Now().UnixNano())
		p.mu.Lock()
		p.codes[code] = codeInfo{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), client: q.Get("client_id"), resource: q.Get("resource")}
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// approve plays the user: opens the authorization URL, follows nothing, and returns the callback's state and code.
func approve(t *testing.T, authURL string) (state, code string) {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorization request refused: HTTP %d", resp.StatusCode)
	}
	return loc.Query().Get("state"), loc.Query().Get("code")
}

func TestOAuthSignInFlowAndRefresh(t *testing.T) {
	d := testutil.DB(t)
	prov := newProvider(t)
	reg := tools.NewRegistry(d.Pool)
	m := NewManager(d.Pool, reg)
	m.AllowPrivate = func() bool { return true } // the fake provider is on loopback
	ctx := context.Background()
	id, err := m.Save(ctx, Server{Name: "secure", Transport: "http", URL: prov.URL + "/mcp", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// 1. not signed in: the server answers 401 and PRISM reports "needs sign-in", not a generic error
	if err := m.Reload(ctx, id); err == nil {
		t.Fatal("connecting without a token must fail")
	}
	if st := m.Statuses()[id]; st.State != "needs_auth" {
		t.Fatalf("status: %+v", st)
	}

	// 2. start: discovery, registration as a public PKCE client, an authorization URL with everything the spec asks for
	authURL, err := m.OAuthStart(ctx, id, "http://127.0.0.1:7777")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("client_id") != "client-1" || q.Get("redirect_uri") != "http://127.0.0.1:7777/oauth/callback" || q.Get("code_challenge_method") != "S256" ||
		q.Get("resource") != prov.URL+"/mcp" || q.Get("scope") != "tools.read" || q.Get("state") == "" || len(q.Get("code_challenge")) < 40 {
		t.Fatalf("authorization URL: %s", authURL)
	}
	if len(prov.registered) != 1 || prov.registered[0]["token_endpoint_auth_method"] != "none" || prov.registered[0]["client_name"] != "PRISM" {
		t.Fatalf("registration: %v", prov.registered)
	}

	// 3. the user approves; the callback exchanges the code (with the verifier) and connects
	state, code := approve(t, authURL)
	name, err := m.OAuthCallback(ctx, state, code)
	if err != nil || name != "secure" {
		t.Fatalf("callback: %q %v", name, err)
	}
	waitFor(t, "connected with the token", func() bool { return m.Statuses()[id].State == "connected" })
	tool, ok := reg.Get("mcp__secure__whoami")
	if !ok {
		t.Fatalf("the server's tools must appear after sign-in: %v", m.Statuses()[id])
	}
	if out, err := tool.Run(ctx, &tools.Env{}, json.RawMessage(`{}`)); err != nil || out != "signed-in user" {
		t.Fatalf("tool call: %q %v", out, err)
	}
	if srvs, _ := m.List(ctx); !srvs[0].SignedIn {
		t.Fatal("the list must say the server is signed in (and never carry the tokens)")
	}
	if b, _ := json.Marshal(func() any { s, _ := m.List(ctx); return s }()); strings.Contains(string(b), "access-1") || strings.Contains(string(b), "refresh-1") {
		t.Fatal("tokens must never be serialised to the UI")
	}

	// 4. a state works once; a wrong state is refused
	if _, err := m.OAuthCallback(ctx, state, code); err == nil {
		t.Fatal("replaying the callback must fail")
	}
	if _, err := m.OAuthCallback(ctx, "forged-state", "x"); err == nil {
		t.Fatal("a forged state must fail")
	}

	// 5. an expired access token is refreshed transparently (refresh tokens here are single-use)
	st, _ := m.oauthLoad(ctx, id)
	st.Expires = time.Now().Add(-time.Minute)
	_ = m.oauthSave(ctx, id, st)
	tok, err := m.tokenFor(ctx, id, false)
	if err != nil || tok != "access-2" {
		t.Fatalf("refresh: %q %v", tok, err)
	}
	// concurrent callers share one refresh
	st, _ = m.oauthLoad(ctx, id)
	st.Expires = time.Now().Add(-time.Minute)
	_ = m.oauthSave(ctx, id, st)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = m.tokenFor(ctx, id, false) }()
	}
	wg.Wait()
	refreshes := 0
	for _, g := range prov.grants {
		if g == "refresh_token" {
			refreshes++
		}
	}
	if refreshes != 2 {
		t.Fatalf("eight concurrent callers must cause ONE refresh (2 in total), got %d", refreshes)
	}

	// 6. the server revokes the access token mid-session: the 401 triggers a forced refresh and a retry
	prov.mu.Lock()
	for k := range prov.access {
		delete(prov.access, k)
	}
	prov.mu.Unlock()
	if out, err := tool.Run(ctx, &tools.Env{}, json.RawMessage(`{}`)); err != nil || out != "signed-in user" {
		t.Fatalf("a revoked access token must be refreshed and the call retried: %q %v", out, err)
	}

	// 7. the refresh token is rejected: the user has to sign in again, and it says so
	prov.mu.Lock()
	for k := range prov.access {
		delete(prov.access, k)
	}
	for k := range prov.refreshOK {
		delete(prov.refreshOK, k)
	}
	prov.mu.Unlock()
	if _, err := tool.Run(ctx, &tools.Env{}, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "sign in") {
		t.Fatalf("dead refresh token: %v", err)
	}

	// 8. sign out forgets everything; changing the server URL also drops the tokens
	if err := m.OAuthSignOut(ctx, id); err != nil {
		t.Fatal(err)
	}
	if srvs, _ := m.List(ctx); srvs[0].SignedIn || m.Statuses()[id].State != "needs_auth" {
		t.Fatal("signed out")
	}
}

func TestOAuthEdgeCases(t *testing.T) {
	d := testutil.DB(t)
	prov := newProvider(t)
	m := NewManager(d.Pool, tools.NewRegistry(d.Pool))
	ctx := context.Background()
	id, _ := m.Save(ctx, Server{Name: "secure", Transport: "http", URL: prov.URL + "/mcp", Enabled: true})

	// SSRF: by default the discovery documents of a server on loopback are refused
	if _, err := m.OAuthStart(ctx, id, "http://127.0.0.1:7777"); err == nil || !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "refus") {
		t.Fatalf("private discovery must be refused by default: %v", err)
	}
	m.AllowPrivate = func() bool { return true }

	// a provider without automatic registration cannot be signed into automatically
	prov.noReg = true
	if _, err := m.OAuthStart(ctx, id, "http://127.0.0.1:7777"); err == nil || !strings.Contains(err.Error(), "registration") {
		t.Fatalf("no registration endpoint: %v", err)
	}
	prov.noReg = false

	// stdio servers have no sign-in; changing the URL invalidates stored tokens
	stdio, _ := m.Save(ctx, Server{Name: "local", Transport: "stdio", Command: "true", Enabled: false})
	if _, err := m.OAuthStart(ctx, stdio, "http://127.0.0.1:7777"); err == nil {
		t.Fatal("stdio servers cannot sign in")
	}
	_ = m.oauthSave(ctx, id, OAuthState{ClientID: "c", Access: "a", Refresh: "r"})
	if s, _ := m.Get(ctx, id); !s.SignedIn {
		t.Fatal("precondition")
	}
	moved := Server{ID: id, Name: "secure", Transport: "http", URL: prov.URL + "/other", Enabled: true}
	if _, err := m.Save(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(ctx, id); s.SignedIn {
		t.Fatal("tokens must not follow a server to a different URL")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
