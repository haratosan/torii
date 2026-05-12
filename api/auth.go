package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/haratosan/torii/store"
)

// apiUserCtxKey scopes the authenticated APIUser into request context so
// handlers don't need to repeat the lookup.
type apiUserCtxKey struct{}

// fromContext returns the authenticated user attached by authMiddleware.
func apiUserFromContext(ctx context.Context) (*store.APIUser, bool) {
	u, ok := ctx.Value(apiUserCtxKey{}).(*store.APIUser)
	return u, ok
}

// authMiddleware extracts a Bearer token, looks up the api_users row, and
// rejects with OpenAI-shaped 401 on any failure. Disabled users are treated
// as if their token didn't exist — opaque to the caller.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeAuthError(w, "missing or malformed Authorization header")
			return
		}
		u, err := s.db.GetAPIUserByToken(token)
		if err != nil {
			s.logger.Error("api auth lookup", "error", err)
			writeAuthError(w, "internal auth error")
			return
		}
		if u == nil || !u.Enabled {
			writeAuthError(w, "invalid or disabled bearer token")
			return
		}
		ctx := context.WithValue(r.Context(), apiUserCtxKey{}, u)
		next(w, r.WithContext(ctx))
	}
}

// bearerToken parses the Authorization header. Accepts both "Bearer xxx" and
// raw "xxx" since some clients drop the prefix.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(h)
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	resp := errorResponse{}
	resp.Error.Message = msg
	resp.Error.Type = "invalid_request_error"
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := errorResponse{}
	resp.Error.Message = msg
	resp.Error.Type = "api_error"
	_ = json.NewEncoder(w).Encode(resp)
}

// NewBearerToken generates a fresh token string. Used by the api-admin
// builtin for create/rotate. Format: "torii_" + 64 hex chars (32 bytes).
func NewBearerToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand can fail on extremely broken systems; in that case we
		// can't safely issue a token. Crash via panic — a 500 to the admin
		// is better than a predictable token.
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return "torii_" + hex.EncodeToString(b[:])
}
