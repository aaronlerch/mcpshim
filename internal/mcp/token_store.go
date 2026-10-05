package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/store"
)

// TokenStoreKey binds a stored OAuth token to both the logical server and its
// endpoint. Re-pointing a server name must never send an existing token to the
// replacement URL.
func TokenStoreKey(server config.MCPServer) string {
	resolved := config.ResolveServer(server)
	sum := sha256.Sum256([]byte(resolved.URL))
	return fmt.Sprintf("%s:%x", server.Name, sum)
}

// adoptLegacyToken moves a token stored under the bare server name (the key
// used before tokens were bound to their endpoint) to TokenStoreKey(s). It only
// moves a row when the bound key is empty, so it runs at most once per server
// and never overwrites a token issued for the current endpoint. Without this,
// upgrading would silently log every OAuth server out.
func adoptLegacyToken(dbStore *store.Store, s config.MCPServer) {
	if dbStore == nil {
		return
	}
	moved, err := dbStore.AdoptToken(s.Name, TokenStoreKey(s))
	if err != nil {
		log.Printf("[token:%s] legacy token migration failed: %v", s.Name, err)
		return
	}
	if moved {
		log.Printf("[token:%s] migrated legacy token to endpoint-bound key", s.Name)
	}
}

// hasOAuthState reports whether s has a stored token for its current endpoint
// or stored client credentials. A nil store has no state.
func hasOAuthState(dbStore *store.Store, s config.MCPServer) bool {
	if dbStore == nil {
		return false
	}
	adoptLegacyToken(dbStore, s)
	return dbStore.HasOAuthState(s.Name, TokenStoreKey(s))
}

type sqliteTokenStore struct {
	store      *store.Store
	serverName string
}

func newSQLiteTokenStore(dbStore *store.Store, serverName string) *sqliteTokenStore {
	return &sqliteTokenStore{store: dbStore, serverName: serverName}
}

func (s *sqliteTokenStore) GetToken(ctx context.Context) (*client.Token, error) {
	if err := ctx.Err(); err != nil {
		log.Printf("[token:%s] GetToken: context already cancelled: %v", s.serverName, err)
		return nil, err
	}
	if s.store == nil {
		log.Printf("[token:%s] GetToken: no backing store, returning ErrNoToken", s.serverName)
		return nil, transport.ErrNoToken
	}
	token, err := s.store.GetToken(s.serverName)
	if err != nil {
		log.Printf("[token:%s] GetToken: store error: %v", s.serverName, err)
		return nil, err
	}
	if token == nil {
		log.Printf("[token:%s] GetToken: no token found in store", s.serverName)
		return nil, transport.ErrNoToken
	}
	hasRefresh := token.RefreshToken != ""
	expiresIn := time.Duration(token.ExpiresIn) * time.Second
	log.Printf("[token:%s] GetToken: found token (type=%s, has_refresh=%v, expires_in=%s, token_prefix=%s…)",
		s.serverName, token.TokenType, hasRefresh, expiresIn, tokenPrefix(token.AccessToken))
	return token, nil
}

func (s *sqliteTokenStore) SaveToken(ctx context.Context, token *client.Token) error {
	if err := ctx.Err(); err != nil {
		log.Printf("[token:%s] SaveToken: context already cancelled: %v", s.serverName, err)
		return err
	}
	if s.store == nil {
		log.Printf("[token:%s] SaveToken: no backing store", s.serverName)
		return fmt.Errorf("sqlite store is not available")
	}
	hasRefresh := token != nil && token.RefreshToken != ""
	expiresIn := time.Duration(0)
	prefix := ""
	if token != nil {
		expiresIn = time.Duration(token.ExpiresIn) * time.Second
		prefix = tokenPrefix(token.AccessToken)
	}
	log.Printf("[token:%s] SaveToken: saving (type=%s, has_refresh=%v, expires_in=%s, token_prefix=%s…)",
		s.serverName, token.TokenType, hasRefresh, expiresIn, prefix)
	return s.store.SaveToken(s.serverName, token)
}

func (s *sqliteTokenStore) String() string {
	return fmt.Sprintf("sqliteTokenStore(%s)", s.serverName)
}

// tokenPrefix returns first 8 chars of a token for log correlation without exposing the full secret.
func tokenPrefix(t string) string {
	if len(t) <= 8 {
		return t
	}
	return t[:8]
}
