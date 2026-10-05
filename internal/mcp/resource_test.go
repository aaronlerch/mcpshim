package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
)

// fakeAuthServer is an MCP server and its authorization server on one origin.
// prm controls what /.well-known/oauth-protected-resource returns: nil means
// 404, otherwise it is served as JSON (with authorization_servers filled in).
type fakeAuthServer struct {
	*httptest.Server
	prm map[string]any

	mu          sync.Mutex
	tokenForms  []url.Values
	prmRequests []string
}

func newFakeAuthServer(t *testing.T, prm map[string]any) *fakeAuthServer {
	t.Helper()
	f := &fakeAuthServer{prm: prm}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/", f.serveProtectedResource)
	mux.HandleFunc("/.well-known/oauth-protected-resource", f.serveProtectedResource)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                   f.URL,
			"authorization_endpoint":   f.URL + "/authorize",
			"token_endpoint":           f.URL + "/token",
			"registration_endpoint":    f.URL + "/register",
			"response_types_supported": []string{"code"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "example-access",
			"token_type":    "Bearer",
			"refresh_token": "example-refresh",
			"expires_in":    3600,
		})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAuthServer) serveProtectedResource(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.prmRequests = append(f.prmRequests, r.URL.Path)
	f.mu.Unlock()
	if f.prm == nil {
		http.NotFound(w, r)
		return
	}
	body := map[string]any{"authorization_servers": []string{f.URL}}
	for k, v := range f.prm {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeAuthServer) forms() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.tokenForms...)
}

// runFlow drives mcp-go's own OAuthHandler -- the code that sends the real
// requests -- through authorize, code exchange and refresh, with mcpshim's
// resource wiring. It returns the authorization URL as the user would see it.
func runFlow(t *testing.T, serverURL string) string {
	t.Helper()
	ctx := context.Background()
	resolver := newResourceResolver(serverURL)
	handler := transport.NewOAuthHandler(mcpclient.OAuthConfig{
		ClientID:    "example-client",
		RedirectURI: "http://127.0.0.1:1/oauth/callback",
		PKCEEnabled: true,
		TokenStore:  transport.NewMemoryTokenStore(),
		HTTPClient:  oauthHTTPClient(resolver),
	})
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	// What mcp-go's streamable HTTP transport does with the server URL.
	handler.SetBaseURL(parsed.Scheme + "://" + parsed.Host)

	authURL, err := handler.GetAuthorizationURL(ctx, "example-state", "example-challenge")
	if err != nil {
		t.Fatal(err)
	}
	authURL, err = withResourceParam(authURL, resolver.resolve(ctx))
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.ProcessAuthorizationResponse(ctx, "example-code", "example-state", "example-verifier"); err != nil {
		t.Fatalf("code exchange: %v", err)
	}
	if _, err := handler.RefreshToken(ctx, "example-refresh"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return authURL
}

func assertResourceEverywhere(t *testing.T, f *fakeAuthServer, authURL, want string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query()["resource"]; len(got) != 1 || got[0] != want {
		t.Fatalf("authorize resource = %q, want [%q] (url %s)", got, want, authURL)
	}
	forms := f.forms()
	if len(forms) != 2 {
		t.Fatalf("token requests = %d, want 2 (exchange + refresh)", len(forms))
	}
	for i, grant := range []string{"authorization_code", "refresh_token"} {
		if forms[i].Get("grant_type") != grant {
			t.Fatalf("token request %d grant_type = %q, want %q", i, forms[i].Get("grant_type"), grant)
		}
		if got := forms[i]["resource"]; len(got) != 1 || got[0] != want {
			t.Fatalf("%s resource = %q, want [%q]", grant, got, want)
		}
	}
}

func TestResourceFromProtectedResourceMetadata(t *testing.T) {
	f := newFakeAuthServer(t, map[string]any{})
	published := f.URL + "/mcp"
	f.prm["resource"] = published

	authURL := runFlow(t, f.URL+"/mcp")
	assertResourceEverywhere(t, f, authURL, published)

	// RFC 9728 path insertion is tried. (mcp-go v0.44.0 makes its own
	// root-only request for authorization-server discovery, so the root path
	// appears too.)
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, p := range f.prmRequests {
		if p == "/.well-known/oauth-protected-resource/mcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("protected-resource requests = %q, want the path-inserted location", f.prmRequests)
	}
}

func TestResourceFallsBackToCanonicalURLWithoutMetadata(t *testing.T) {
	f := newFakeAuthServer(t, nil)
	// Uppercase scheme and host and a fragment, to exercise canonicalization.
	configured := strings.Replace(f.URL, "http://127.0.0.1", "HTTP://LOCALHOST", 1) + "/MCP/v1#frag"
	if !strings.HasPrefix(configured, "HTTP://LOCALHOST:") {
		t.Fatalf("unexpected test server URL %q", f.URL)
	}
	want := strings.Replace(f.URL, "http://127.0.0.1", "http://localhost", 1) + "/MCP/v1"

	authURL := runFlow(t, configured)
	assertResourceEverywhere(t, f, authURL, want)
}

func TestResourceFallsBackWhenMetadataOmitsResource(t *testing.T) {
	f := newFakeAuthServer(t, map[string]any{})
	authURL := runFlow(t, f.URL+"/mcp")
	assertResourceEverywhere(t, f, authURL, f.URL+"/mcp")
}

func TestResourceIgnoresMetadataNamingAnotherServer(t *testing.T) {
	f := newFakeAuthServer(t, map[string]any{"resource": "https://other.example.com/mcp"})
	authURL := runFlow(t, f.URL+"/mcp")
	assertResourceEverywhere(t, f, authURL, f.URL+"/mcp")
}

func TestCanonicalResourceURI(t *testing.T) {
	cases := map[string]string{
		"HTTPS://MCP.Example.COM/mcp":       "https://mcp.example.com/mcp",
		"https://mcp.example.com/":          "https://mcp.example.com",
		"https://mcp.example.com/a/b#frag":  "https://mcp.example.com/a/b",
		"https://mcp.example.com:8443/Mcp/": "https://mcp.example.com:8443/Mcp/",
	}
	for in, want := range cases {
		got, err := canonicalResourceURI(in)
		if err != nil || got != want {
			t.Errorf("canonicalResourceURI(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := canonicalResourceURI("/relative"); err == nil {
		t.Error("relative URL accepted")
	}
}

func TestResourceCoversServer(t *testing.T) {
	server := "https://mcp.example.com/tenant/mcp"
	for resource, want := range map[string]bool{
		"https://mcp.example.com/tenant/mcp": true,
		"https://MCP.example.com/tenant/mcp": true,
		"https://mcp.example.com":            true,
		"https://mcp.example.com/tenant":     true,
		"https://mcp.example.com/ten":        false,
		"https://mcp.example.com/other":      false,
		"http://mcp.example.com/tenant/mcp":  false,
		"https://evil.example.com/tenant":    false,
	} {
		if got := resourceCoversServer(resource, server); got != want {
			t.Errorf("resourceCoversServer(%q) = %v, want %v", resource, got, want)
		}
	}
}

func TestWithResourceParamKeepsExisting(t *testing.T) {
	in := "https://auth.example.com/authorize?client_id=x&resource=https%3A%2F%2Fa.example.com"
	got, err := withResourceParam(in, "https://b.example.com")
	if err != nil || got != in {
		t.Fatalf("withResourceParam = %q, %v; want unchanged", got, err)
	}
}
