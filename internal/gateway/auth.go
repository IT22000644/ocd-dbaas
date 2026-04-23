package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Roles define what actions a key holder can perform.
const (
	RoleAdmin    = "admin"    // full access: create, delete, start, stop, list, get
	RoleOperator = "operator" // operational: start, stop, list, get
	RoleViewer   = "viewer"   // read-only: list, get
)

// contextKey is an unexported type to avoid collisions in context values.
type contextKey string

const roleContextKey contextKey = "dbaas-role"

// RoleFromContext returns the authenticated role, or empty string if unauthenticated.
func RoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(roleContextKey).(string); ok {
		return v
	}
	return ""
}

// authMiddleware validates Bearer tokens against API keys stored in a K8s Secret.
//
// The Secret (configured via secretName/secretNamespace) maps API keys to roles:
//
//	data:
//	  <api-key-1>: admin
//	  <api-key-2>: operator
//	  <api-key-3>: viewer
//
// The middleware caches the Secret in memory and reloads it every time the
// cache is invalidated (on 401 from an unknown key, max once per 5 seconds).
type authMiddleware struct {
	k8sClient       client.Client
	secretName      string
	secretNamespace string

	mu   sync.RWMutex
	keys map[string]string // api-key → role
}

func newAuthMiddleware(k8sClient client.Client, secretName, secretNamespace string) *authMiddleware {
	return &authMiddleware{
		k8sClient:       k8sClient,
		secretName:      secretName,
		secretNamespace: secretNamespace,
		keys:            make(map[string]string),
	}
}

// loadKeys reads the API key Secret from Kubernetes.
func (a *authMiddleware) loadKeys() {
	ctx := context.Background()
	var secret corev1.Secret
	if err := a.k8sClient.Get(ctx, types.NamespacedName{
		Name:      a.secretName,
		Namespace: a.secretNamespace,
	}, &secret); err != nil {
		log.Log.Error(err, "failed to load API key secret", "secret", a.secretName)
		return
	}

	keys := make(map[string]string, len(secret.Data))
	for key, val := range secret.Data {
		role := strings.TrimSpace(string(val))
		if role == RoleAdmin || role == RoleOperator || role == RoleViewer {
			keys[key] = role
		}
	}

	a.mu.Lock()
	a.keys = keys
	a.mu.Unlock()

	log.Log.Info("loaded API keys", "count", len(keys))
}

// lookup returns the role for a given API key, reloading keys if not found.
func (a *authMiddleware) lookup(token string) (string, bool) {
	a.mu.RLock()
	role, ok := a.keys[token]
	a.mu.RUnlock()
	if ok {
		return role, true
	}

	// Key not found — reload the Secret in case new keys were added.
	a.loadKeys()

	a.mu.RLock()
	role, ok = a.keys[token]
	a.mu.RUnlock()
	return role, ok
}

// Wrap returns an http.Handler that enforces authentication.
// The /healthz endpoint is excluded from auth.
func (a *authMiddleware) Wrap(next http.Handler) http.Handler {
	// Load keys eagerly on first request path.
	a.loadKeys()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health checks.
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dbaas"`)
			writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			// No "Bearer " prefix found.
			writeError(w, http.StatusUnauthorized, "invalid Authorization header format, expected: Bearer <token>")
			return
		}

		role, ok := a.lookup(token)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Store role in context for authorization checks in handlers.
		ctx := context.WithValue(r.Context(), roleContextKey, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns true if the caller's role is authorized for the action.
// Call this in handlers to enforce authorization.
func RequireRole(w http.ResponseWriter, r *http.Request, allowedRoles ...string) bool {
	role := RoleFromContext(r.Context())
	for _, allowed := range allowedRoles {
		if role == allowed {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "insufficient permissions, required role: "+strings.Join(allowedRoles, " or "))
	return false
}
