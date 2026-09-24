package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"prism/internal/netguard"
)

// OAuth for remote MCP servers (the MCP authorization spec): the server answers 401 and points at its
// protected-resource metadata; PRISM discovers the authorization server, registers itself as a public client
// (RFC 7591), sends the user through the authorization-code flow with PKCE (RFC 7636) and the `resource`
// parameter (RFC 8707), and keeps the tokens, refreshing them when they expire.

// AuthRequiredError is returned when an HTTP MCP server wants a sign-in.
type AuthRequiredError struct {
	URL      string
	Metadata string // resource_metadata URL from WWW-Authenticate, when given
}

func (e *AuthRequiredError) Error() string { return "the server needs you to sign in" }

var resMetaRe = regexp.MustCompile(`resource_metadata="?([^",\s]+)"?`)

func authRequired(u string, h http.Header) *AuthRequiredError {
	e := &AuthRequiredError{URL: u}
	if m := resMetaRe.FindStringSubmatch(h.Get("WWW-Authenticate")); m != nil {
		e.Metadata = m[1]
	}
	return e
}

// OAuthState is what PRISM keeps per server (JSON in mcp_servers.oauth).
type OAuthState struct {
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	AuthURL      string    `json:"auth_url,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	Resource     string    `json:"resource,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	RedirectURI  string    `json:"redirect_uri,omitempty"`
	Access       string    `json:"access,omitempty"`
	Refresh      string    `json:"refresh,omitempty"`
	Expires      time.Time `json:"expires,omitempty"`
}

func (s OAuthState) signedIn() bool { return s.Access != "" || s.Refresh != "" }

type pendingAuth struct {
	server   int64
	verifier string
	redirect string
	created  time.Time
}

// oauth holds the in-flight authorizations (state → verifier) for one Manager.
type oauthFlows struct {
	mu      sync.Mutex
	pending map[string]pendingAuth
}

func (f *oauthFlows) put(state string, p pendingAuth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil {
		f.pending = map[string]pendingAuth{}
	}
	for k, v := range f.pending { // forget abandoned sign-ins
		if time.Since(v.created) > 15*time.Minute {
			delete(f.pending, k)
		}
	}
	f.pending[state] = p
}

// take returns and removes a pending authorization: a state works exactly once.
func (f *oauthFlows) take(state string) (pendingAuth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pending[state]
	delete(f.pending, state)
	if ok && time.Since(p.created) > 15*time.Minute {
		return p, false
	}
	return p, ok
}

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (m *Manager) httpc() *http.Client {
	allow := m.AllowPrivate != nil && m.AllowPrivate()
	return netguard.Client(allow, 30*time.Second)
}

func getJSON(ctx context.Context, hc *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func origin(u string) (string, error) {
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return "", fmt.Errorf("bad URL %q", u)
	}
	return p.Scheme + "://" + p.Host, nil
}

type discovered struct {
	AuthURL, TokenURL, RegURL, Resource, Scope string
}

// discover finds the authorization server of an MCP endpoint.
func (m *Manager) discover(ctx context.Context, mcpURL, metaURL string) (*discovered, error) {
	hc := m.httpc()
	org, err := origin(mcpURL)
	if err != nil {
		return nil, err
	}
	if metaURL == "" {
		metaURL = org + "/.well-known/oauth-protected-resource"
	}
	var pr struct {
		Resource string   `json:"resource"`
		AS       []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
	}
	asBase := org // servers that do not publish resource metadata are their own authorization server
	if err := getJSON(ctx, hc, metaURL, &pr); err == nil && len(pr.AS) > 0 {
		asBase = strings.TrimRight(pr.AS[0], "/")
	}
	var meta struct {
		Auth  string   `json:"authorization_endpoint"`
		Token string   `json:"token_endpoint"`
		Reg   string   `json:"registration_endpoint"`
		Scope []string `json:"scopes_supported"`
		PKCE  []string `json:"code_challenge_methods_supported"`
	}
	var last error
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		if last = getJSON(ctx, hc, asBase+p, &meta); last == nil && meta.Auth != "" && meta.Token != "" {
			break
		}
	}
	if meta.Auth == "" || meta.Token == "" {
		return nil, fmt.Errorf("cannot find the authorization server for %s: %v", mcpURL, last)
	}
	if len(meta.PKCE) > 0 {
		ok := false
		for _, c := range meta.PKCE {
			ok = ok || c == "S256"
		}
		if !ok {
			return nil, errors.New("the authorization server does not support PKCE (S256), which PRISM requires")
		}
	}
	d := &discovered{AuthURL: meta.Auth, TokenURL: meta.Token, RegURL: meta.Reg, Resource: pr.Resource, Scope: strings.Join(firstNonEmptyList(pr.Scopes, meta.Scope), " ")}
	if d.Resource == "" {
		d.Resource = mcpURL
	}
	return d, nil
}

func firstNonEmptyList(a ...[]string) []string {
	for _, x := range a {
		if len(x) > 0 {
			return x
		}
	}
	return nil
}

// register creates a public client at the authorization server (dynamic client registration).
func (m *Manager) register(ctx context.Context, regURL, redirect string) (id, secret string, err error) {
	if regURL == "" {
		return "", "", errors.New("the server does not support automatic registration: add a client id in the server's headers/config first")
	}
	body, _ := json.Marshal(map[string]any{"client_name": "PRISM", "redirect_uris": []string{redirect}, "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, regURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpc().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("client registration refused (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(b[:min(len(b), 200)])))
	}
	var r struct {
		ClientID string `json:"client_id"`
		Secret   string `json:"client_secret"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.ClientID == "" {
		return "", "", errors.New("the registration reply had no client id")
	}
	return r.ClientID, r.Secret, nil
}

// OAuthStart begins a sign-in for a server and returns the URL to open in the browser. redirectBase is the
// origin the user reaches PRISM on (the callback is redirectBase + /oauth/callback).
func (m *Manager) OAuthStart(ctx context.Context, id int64, redirectBase string) (string, error) {
	srv, err := m.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if srv.Transport != "http" || srv.URL == "" {
		return "", errors.New("only servers reached over http can use sign-in")
	}
	redirect := strings.TrimRight(redirectBase, "/") + "/oauth/callback"
	// ask the server what it wants (and where its metadata lives) by knocking without credentials
	metaURL := ""
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil); err == nil {
		if resp, err := m.httpc().Do(req); err == nil {
			if resp.StatusCode == http.StatusUnauthorized {
				metaURL = authRequired(srv.URL, resp.Header).Metadata
			}
			resp.Body.Close()
		}
	}
	d, err := m.discover(ctx, srv.URL, metaURL)
	if err != nil {
		return "", err
	}
	st, _ := m.oauthLoad(ctx, id)
	if st.ClientID == "" || st.RedirectURI != redirect || st.AuthURL != d.AuthURL {
		cid, secret, err := m.register(ctx, d.RegURL, redirect)
		if err != nil {
			return "", err
		}
		st = OAuthState{ClientID: cid, ClientSecret: secret}
	}
	st.AuthURL, st.TokenURL, st.Resource, st.Scope, st.RedirectURI = d.AuthURL, d.TokenURL, d.Resource, d.Scope, redirect
	if err := m.oauthSave(ctx, id, st); err != nil {
		return "", err
	}
	verifier, state := randB64(48), randB64(24)
	sum := sha256.Sum256([]byte(verifier))
	m.flows.put(state, pendingAuth{server: id, verifier: verifier, redirect: redirect, created: time.Now()})
	q := url.Values{"response_type": {"code"}, "client_id": {st.ClientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {st.Resource}}
	if st.Scope != "" {
		q.Set("scope", st.Scope)
	}
	sep := "?"
	if strings.Contains(st.AuthURL, "?") {
		sep = "&"
	}
	return st.AuthURL + sep + q.Encode(), nil
}

type tokenReply struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Expires int    `json:"expires_in"`
	Error   string `json:"error"`
	Desc    string `json:"error_description"`
}

func (m *Manager) tokenRequest(ctx context.Context, st OAuthState, form url.Values) (*tokenReply, error) {
	form.Set("client_id", st.ClientID)
	if st.ClientSecret != "" {
		form.Set("client_secret", st.ClientSecret)
	}
	if st.Resource != "" {
		form.Set("resource", st.Resource)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, st.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpc().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r tokenReply
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r)
	if resp.StatusCode/100 != 2 || r.Access == "" {
		msg := r.Error
		if r.Desc != "" {
			msg += ": " + r.Desc
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("the token request failed: %s", msg)
	}
	return &r, nil
}

func (r *tokenReply) apply(st *OAuthState) {
	st.Access = r.Access
	if r.Refresh != "" {
		st.Refresh = r.Refresh
	}
	st.Expires = time.Time{}
	if r.Expires > 0 {
		st.Expires = time.Now().Add(time.Duration(r.Expires) * time.Second)
	}
}

// OAuthCallback finishes a sign-in: the browser came back with a code. It returns the server's name.
func (m *Manager) OAuthCallback(ctx context.Context, state, code string) (string, error) {
	p, ok := m.flows.take(state)
	if !ok {
		return "", errors.New("this sign-in link has expired or was already used: start the sign-in again from PRISM")
	}
	if code == "" {
		return "", errors.New("the provider returned no authorization code")
	}
	st, err := m.oauthLoad(ctx, p.server)
	if err != nil {
		return "", err
	}
	tr, err := m.tokenRequest(ctx, st, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.redirect}, "code_verifier": {p.verifier}})
	if err != nil {
		return "", err
	}
	tr.apply(&st)
	if err := m.oauthSave(ctx, p.server, st); err != nil {
		return "", err
	}
	srv, err := m.Get(ctx, p.server)
	if err != nil {
		return "", err
	}
	go func() { _ = m.Reload(context.Background(), p.server) }() // connect with the new token
	return srv.Name, nil
}

// tokenFor returns a usable access token for a server, refreshing it when it is about to expire (or when force).
func (m *Manager) tokenFor(ctx context.Context, id int64, force bool) (string, error) {
	m.tokMu.Lock()
	defer m.tokMu.Unlock() // one refresh at a time: refresh tokens are often single-use
	st, err := m.oauthLoad(ctx, id)
	if err != nil {
		return "", err
	}
	if !st.signedIn() {
		return "", nil
	}
	fresh := st.Access != "" && (st.Expires.IsZero() || time.Until(st.Expires) > 45*time.Second)
	if fresh && !force {
		return st.Access, nil
	}
	if st.Refresh == "" {
		if force || !fresh {
			return "", &AuthRequiredError{}
		}
		return st.Access, nil
	}
	tr, err := m.tokenRequest(ctx, st, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {st.Refresh}})
	if err != nil {
		return "", &AuthRequiredError{}
	}
	tr.apply(&st)
	_ = m.oauthSave(ctx, id, st)
	return st.Access, nil
}

// OAuthSignOut forgets the tokens (and the registration) of a server.
func (m *Manager) OAuthSignOut(ctx context.Context, id int64) error {
	m.disconnect(id)
	if err := m.oauthSave(ctx, id, OAuthState{}); err != nil {
		return err
	}
	m.setStatus(id, Status{State: "needs_auth", Error: "signed out"})
	return nil
}

func (m *Manager) oauthLoad(ctx context.Context, id int64) (OAuthState, error) {
	var b []byte
	var st OAuthState
	if err := m.db.QueryRow(ctx, `SELECT oauth FROM mcp_servers WHERE id=$1`, id).Scan(&b); err != nil {
		return st, err
	}
	_ = json.Unmarshal(b, &st)
	return st, nil
}

func (m *Manager) oauthSave(ctx context.Context, id int64, st OAuthState) error {
	b, _ := json.Marshal(st)
	_, err := m.db.Exec(ctx, `UPDATE mcp_servers SET oauth=$2 WHERE id=$1`, id, b)
	return err
}
