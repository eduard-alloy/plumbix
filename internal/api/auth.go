package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireToken rejects requests that don't carry the server's bearer token.
// The response never says whether the token was missing or wrong.
func requireToken(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(want) == 0 || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid API token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
