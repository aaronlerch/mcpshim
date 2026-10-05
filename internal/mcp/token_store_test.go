package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/store"
)

func TestTokenStoreKeyBindsTokenToServerURL(t *testing.T) {
	one := TokenStoreKey(config.MCPServer{Name: "example", URL: "https://one.example.com/mcp"})
	same := TokenStoreKey(config.MCPServer{Name: "example", URL: "https://one.example.com/mcp"})
	two := TokenStoreKey(config.MCPServer{Name: "example", URL: "https://two.example.com/mcp"})

	if one != same {
		t.Fatalf("token key is not stable: %q != %q", one, same)
	}
	if one == two {
		t.Fatalf("different endpoints share token key %q", one)
	}
	if strings.Contains(one, "https://") {
		t.Fatalf("token key exposes endpoint: %q", one)
	}
}

func TestLegacyTokenIsAdoptedOnceUnderEndpointKey(t *testing.T) {
	dbStore, err := store.Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	server := config.MCPServer{Name: "example", URL: "https://mcp.example.com/mcp"}
	// A token written by a build that keyed tokens by bare server name.
	if err := dbStore.SaveToken(server.Name, &mcpclient.Token{AccessToken: "legacy"}); err != nil {
		t.Fatal(err)
	}

	if !hasOAuthState(dbStore, server) {
		t.Fatal("legacy token was not recognized as OAuth state")
	}
	got, err := dbStore.GetToken(TokenStoreKey(server))
	if err != nil || got == nil || got.AccessToken != "legacy" {
		t.Fatalf("bound token = %#v, %v; want the adopted legacy token", got, err)
	}
	if old, _ := dbStore.GetToken(server.Name); old != nil {
		t.Fatalf("legacy row still present after adoption: %#v", old)
	}

	// A later legacy row must never overwrite a token already bound to the
	// endpoint.
	if err := dbStore.SaveToken(server.Name, &mcpclient.Token{AccessToken: "stale"}); err != nil {
		t.Fatal(err)
	}
	adoptLegacyToken(dbStore, server)
	got, _ = dbStore.GetToken(TokenStoreKey(server))
	if got == nil || got.AccessToken != "legacy" {
		t.Fatalf("bound token overwritten: %#v", got)
	}
}

func TestNilStoreHasNoOAuthState(t *testing.T) {
	if hasOAuthState(nil, config.MCPServer{Name: "example", URL: "https://mcp.example.com/mcp"}) {
		t.Fatal("nil store reported OAuth state")
	}
}
