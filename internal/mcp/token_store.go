package mcp

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mcpshim/mcpshim/internal/store"
)

type sqliteTokenStore struct {
	store      *store.Store
	serverName string
}

func newSQLiteTokenStore(dbStore *store.Store, serverName string) transport.TokenStore {
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
