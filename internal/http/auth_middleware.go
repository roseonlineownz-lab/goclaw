package http

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

// --- JWT keyset (package-level, initialized at startup via InitJWTKeyset) ---

var pkgJWTKeyset *auth.JWTKeyset

// InitJWTKeyset sets the JWT keyset for HTTP auth. Must be called once at startup.
func InitJWTKeyset(ks *auth.JWTKeyset) { pkgJWTKeyset = ks }

// resolveJWTBearer validates a bearer token as a JWT access token.
// Returns (claims, true) on success; (nil, false) if not a valid JWT (caller should
// fall through to API-key path).
func resolveJWTBearer(bearer string) (*auth.Claims, bool) {
	return ResolveJWTAccess(bearer)
}

// ResolveJWTAccess is the exported version of resolveJWTBearer used by the WS
// gateway (internal/gateway/router.go) for the connect-frame accessToken path.
func ResolveJWTAccess(token string) (*auth.Claims, bool) {
	if pkgJWTKeyset == nil || token == "" {
		return nil, false
	}
	claims, err := auth.VerifyAccess(pkgJWTKeyset, token)
	if err != nil {
		return nil, false
	}
	return claims, true
}

// --- Bootstrap required flag ---

var (
	bootstrapMu       sync.RWMutex
	bootstrapRequired bool
)

// SetBootstrapRequired toggles the bootstrap-required flag. Called at:
//   - gateway startup (after counting users)
//   - successful POST /v1/bootstrap/init
func SetBootstrapRequired(required bool) {
	bootstrapMu.Lock()
	bootstrapRequired = required
	bootstrapMu.Unlock()
}

// IsBootstrapRequired returns true when no root user exists yet.
func IsBootstrapRequired() bool {
	bootstrapMu.RLock()
	defer bootstrapMu.RUnlock()
	return bootstrapRequired
}

// --- Bootstrap token (in-memory; never persisted to DB or disk) ---

var (
	bootstrapTokenMu sync.RWMutex
	bootstrapToken   string // 32 hex chars (16 bytes)
)

// GenerateBootstrapToken creates a 32-byte hex token and stores it in memory.
// Called at startup when bootstrapRequired=true. Returns the token so the
// caller can print it via slog.Info for operator visibility.
func GenerateBootstrapToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	bootstrapTokenMu.Lock()
	bootstrapToken = tok
	bootstrapTokenMu.Unlock()
	return tok, nil
}

// clearBootstrapToken wipes the in-memory token after a successful bootstrap.
func clearBootstrapToken() {
	bootstrapTokenMu.Lock()
	bootstrapToken = ""
	bootstrapTokenMu.Unlock()
}

// validateBootstrapToken constant-time compares the provided header value
// against the in-memory token. Returns false if token is unset.
func validateBootstrapToken(headerVal string) bool {
	bootstrapTokenMu.RLock()
	expected := bootstrapToken
	bootstrapTokenMu.RUnlock()
	if expected == "" || headerVal == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(headerVal), []byte(expected)) == 1
}

// bootstrapWhitelist paths that bypass the bootstrap-required 503 gate.
var bootstrapWhitelist = []string{
	"/v1/bootstrap/",
	"/healthz",
	"/health",
	"/v1/version",
	"/metrics",
}

// BootstrapRequiredMiddleware wraps the entire mux. When bootstrapRequired=true,
// all paths EXCEPT the whitelist return 503 with a JSON error body.
//
// KISS: we skip the dual-listener spec (separate :port for bootstrap routes)
// and instead apply a loopback check inside the bootstrap handler itself.
// This avoids complexity of managing two net.Listener lifetimes and ensures
// the bootstrap token is the primary security gate, with localhost as a
// defense-in-depth second factor.
func BootstrapRequiredMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsBootstrapRequired() {
			next.ServeHTTP(w, r)
			return
		}
		// Allow whitelisted paths through even when not bootstrapped.
		path := r.URL.Path
		for _, prefix := range bootstrapWhitelist {
			if strings.HasPrefix(path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}
		locale := extractLocale(r)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":   "bootstrap_required",
			"message": i18n.T(locale, i18n.MsgBootstrapRequired),
		})
	})
}

// --- Per-IP login rate limiter ---

type ipBucket struct {
	mu       sync.Mutex
	tokens   int
	lastSeen time.Time
}

var (
	loginRateMu sync.Mutex
	loginRateIP = map[string]*ipBucket{}
)

const (
	loginRateMaxPerMin = 5
	loginRatePeriod    = time.Minute
)

// init starts a background sweeper that drops idle bucket entries to bound
// memory under sustained probing from rotating IPs (e.g. IPv6 /64 walks).
// Sweep runs every 10 minutes; entries older than 2× the rate-limit window
// are evicted.
func init() {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			cutoff := time.Now().Add(-2 * loginRatePeriod)
			loginRateMu.Lock()
			for ip, b := range loginRateIP {
				b.mu.Lock()
				stale := b.lastSeen.Before(cutoff)
				b.mu.Unlock()
				if stale {
					delete(loginRateIP, ip)
				}
			}
			loginRateMu.Unlock()
		}
	}()
}

// loginRateLimitMiddleware applies a 5-req/min per-IP limit on login.
// Returns 429 with Retry-After when exhausted.
func loginRateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := extractClientIP(r)
		if !loginRateAllow(ip) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "rate_limit_exceeded",
			})
			return
		}
		next(w, r)
	}
}

func loginRateAllow(ip string) bool {
	loginRateMu.Lock()
	b, ok := loginRateIP[ip]
	if !ok {
		b = &ipBucket{tokens: loginRateMaxPerMin - 1, lastSeen: time.Now()}
		loginRateIP[ip] = b
		loginRateMu.Unlock()
		return true
	}
	loginRateMu.Unlock()

	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.Sub(b.lastSeen) >= loginRatePeriod {
		// Refill bucket.
		b.tokens = loginRateMaxPerMin - 1
		b.lastSeen = now
		return true
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	b.lastSeen = now // touch on consume so eviction sweeper sees activity
	return true
}

// extractClientIP returns the best-effort IP for rate limiting.
// Does NOT trust proxy headers for security-sensitive decisions;
// uses RemoteAddr as the authoritative source.
func extractClientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// isLoopback returns true if the request comes from a loopback address.
func isLoopback(r *http.Request) bool {
	addr := r.RemoteAddr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// buildJWTAuthResult constructs an authResult from verified JWT claims.
func buildJWTAuthResult(claims *auth.Claims) authResult {
	return authResult{
		Role:          permissions.Role(claims.Role),
		Authenticated: true,
		JWTSub:        claims.Sub,
	}
}
