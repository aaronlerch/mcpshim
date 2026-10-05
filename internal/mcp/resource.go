package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RFC 8707 resource indicators.
//
// The MCP authorization spec requires the client to send `resource` -- the
// MCP server's canonical URI -- on the authorization request and on every
// token request (code exchange and refresh). Without it, an authorization
// server shared by several MCP servers has no way to tell which one the token
// is for, and may issue it with another server's audience; the target then
// rejects it. WorkOS AuthKit does exactly that.
//
// mcp-go v0.44.0 sends no `resource` anywhere. Later releases add it, but only
// when protected-resource metadata is discoverable, so a server without that
// metadata still gets nothing. This file adds it from the outside instead of
// forking the library:
//
//   - the authorization URL is a plain string mcpshim already handles, so the
//     parameter is appended there (withResourceParam);
//   - both token requests go through OAuthConfig.HTTPClient, so a RoundTripper
//     adds it to their form bodies (resourceRoundTripper).
//
// Both add the parameter only when it is absent, so a library upgrade that
// starts sending it does not produce a duplicate.

// resourceMetadataTimeout bounds the protected-resource metadata fetch. It
// happens only on a token request or a login, never on an ordinary call.
const resourceMetadataTimeout = 10 * time.Second

// canonicalResourceURI returns the canonical form of an MCP server URL for use
// as an RFC 8707 resource indicator: lowercase scheme and host, no fragment,
// path kept, and a bare "/" path dropped (the MCP spec prefers the form
// without a trailing slash).
func canonicalResourceURI(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("resource URI %q must be absolute", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	if u.Path == "/" && u.RawQuery == "" {
		u.Path = ""
		u.RawPath = ""
	}
	return u.String(), nil
}

// resourceResolver works out the resource indicator for one MCP server, once.
// It prefers the `resource` the server publishes in its protected-resource
// metadata (RFC 9728) and falls back to the canonical configured URL.
type resourceResolver struct {
	serverURL  string
	httpClient *http.Client

	mu       sync.Mutex
	resolved bool
	value    string
}

func newResourceResolver(serverURL string) *resourceResolver {
	return &resourceResolver{
		serverURL:  serverURL,
		httpClient: &http.Client{Timeout: resourceMetadataTimeout},
	}
}

// resolve returns the resource indicator, or "" if the configured URL cannot
// be parsed (in which case nothing is sent and the flow behaves as before).
func (r *resourceResolver) resolve(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolved {
		return r.value
	}
	fallback, err := canonicalResourceURI(r.serverURL)
	if err != nil {
		log.Printf("[oauth] cannot derive a resource indicator from %q: %v", r.serverURL, err)
		r.resolved = true
		return ""
	}
	r.value = fallback
	if published := r.fetchPublishedResource(ctx, fallback); published != "" {
		r.value = published
	}
	r.resolved = true
	return r.value
}

// fetchPublishedResource reads the protected-resource metadata and returns its
// `resource` if it is one this server may legitimately claim, else "".
func (r *resourceResolver) fetchPublishedResource(ctx context.Context, canonical string) string {
	for _, metadataURL := range protectedResourceMetadataURLs(canonical) {
		resource, ok := r.fetchResourceField(ctx, metadataURL)
		if !ok {
			continue
		}
		if resource == "" {
			// Metadata exists but names no resource: use the configured URL.
			return ""
		}
		if !resourceCoversServer(resource, canonical) {
			// RFC 9728 section 3.3: a resource identifier that does not match
			// the server being addressed must not be used. Otherwise a server
			// could have the user consent to a token for some OTHER resource
			// and receive it.
			log.Printf("[oauth] ignoring protected-resource metadata at %s: resource %q does not cover %q",
				metadataURL, resource, canonical)
			return ""
		}
		return resource
	}
	return ""
}

func (r *resourceResolver) fetchResourceField(ctx context.Context, metadataURL string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, resourceMetadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var metadata struct {
		Resource string `json:"resource"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&metadata); err != nil {
		return "", false
	}
	return strings.TrimSpace(metadata.Resource), true
}

// protectedResourceMetadataURLs lists the RFC 9728 section 3.1 locations for
// serverURL: the well-known suffix inserted before the path first, then at the
// origin root.
func protectedResourceMetadataURLs(serverURL string) []string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	const wellKnown = "/.well-known/oauth-protected-resource"
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	if path == "" {
		return []string{origin + wellKnown}
	}
	return []string{origin + wellKnown + path, origin + wellKnown}
}

// resourceCoversServer reports whether resource identifies serverURL: same
// scheme and host, and a path that is serverURL's path or a segment prefix of
// it (a server at /mcp may publish its origin as the resource).
func resourceCoversServer(resource, serverURL string) bool {
	ru, err := url.Parse(resource)
	if err != nil || ru.Fragment != "" {
		return false
	}
	su, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(ru.Scheme, su.Scheme) || !strings.EqualFold(ru.Host, su.Host) {
		return false
	}
	rp := strings.TrimSuffix(ru.EscapedPath(), "/")
	sp := strings.TrimSuffix(su.EscapedPath(), "/")
	return rp == "" || rp == sp || strings.HasPrefix(sp, rp+"/")
}

// withResourceParam adds resource to an authorization URL unless the URL
// already carries one.
func withResourceParam(authURL, resource string) (string, error) {
	if resource == "" {
		return authURL, nil
	}
	u, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if q.Get("resource") != "" {
		return authURL, nil
	}
	q.Set("resource", resource)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// resourceRoundTripper adds `resource` to OAuth token requests -- form POSTs
// with grant_type authorization_code or refresh_token -- that lack one. Every
// other request passes through untouched.
type resourceRoundTripper struct {
	base     http.RoundTripper
	resolver *resourceResolver
}

func (t *resourceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.Method != http.MethodPost || req.Body == nil ||
		!strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }

	form, err := url.ParseQuery(string(body))
	if err != nil || form.Get("resource") != "" {
		return base.RoundTrip(out)
	}
	switch form.Get("grant_type") {
	case "authorization_code", "refresh_token":
	default:
		return base.RoundTrip(out)
	}
	resource := t.resolver.resolve(req.Context())
	if resource == "" {
		return base.RoundTrip(out)
	}
	form.Set("resource", resource)
	encoded := []byte(form.Encode())
	out.Body = io.NopCloser(bytes.NewReader(encoded))
	out.ContentLength = int64(len(encoded))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(encoded)), nil }
	return base.RoundTrip(out)
}

// oauthHTTPClient is the HTTP client handed to mcp-go's OAuth handler: the
// same 30s timeout mcp-go defaults to, with resource indicators added.
func oauthHTTPClient(resolver *resourceResolver) *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &resourceRoundTripper{resolver: resolver},
	}
}
