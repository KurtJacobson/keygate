package middleware

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

type Claims struct {
	UserID  string `json:"uid"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"adm,omitempty"`
	jwt.RegisteredClaims
}

func IssueJWT(secret, userID, email, name string, isAdmin bool, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:  userID,
		Email:   email,
		Name:    name,
		IsAdmin: isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// AdminChecker checks if a user has admin privileges by user ID.
// Injected at startup — queries the database for the user's role.
type AdminChecker func(ctx context.Context, userID string) bool

// SessionAuth validates a JWT from the Authorization header or session cookie.
// Admin status is checked at request time (from DB, not JWT claims) for security —
// this ensures role changes take effect immediately without waiting for JWT expiry.
func SessionAuth(secret string, adminCheck ...AdminChecker) gin.HandlerFunc {
	var checkAdmin AdminChecker
	if len(adminCheck) > 0 {
		checkAdmin = adminCheck[0]
	}

	return func(c *gin.Context) {
		raw := extractBearer(c)
		if raw == "" {
			if cookie, err := c.Cookie("session"); err == nil && cookie != "" {
				raw = cookie
			}
		}
		if raw == "" {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}

		claims := &Claims{}
		tok, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
			return []byte(secret), nil
		})
		if err != nil || !tok.Valid {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("email", claims.Email)
		c.Set("name", claims.Name)

		// Determine admin status at request time from database role
		isAdmin := false
		if checkAdmin != nil {
			isAdmin = checkAdmin(c.Request.Context(), claims.UserID)
		} else {
			isAdmin = claims.IsAdmin
		}
		c.Set("is_admin", isAdmin)
		c.Next()
	}
}

// SessionOrAPIKey accepts either:
//
//   - `Authorization: Bearer kg_live_<token>` — programmatic credential
//     (api_keys row). Whether the key may access a specific route is
//     decided by the downstream RequireScope middleware, NOT here.
//
//   - Session cookie OR `Authorization: Bearer <JWT>` — interactive
//     admin login (same logic as SessionAuth).
//
// We deliberately don't enforce `admin` scope at this layer anymore.
// Doing so blocks restricted-scope keys (e.g. licenses:write) from
// ever reaching the route that they DO have permission for. Scope
// enforcement is now per-route via RequireScope.
//
// auth_type ("session" or "api_key") is set so RequireScope and
// audit code can tell the two paths apart without re-examining the
// Authorization header.
func SessionOrAPIKey(secret string, db *store.Store, adminCheck AdminChecker) gin.HandlerFunc {
	sessionMW := SessionAuth(secret, adminCheck)
	return func(c *gin.Context) {
		raw := extractBearer(c)
		if strings.HasPrefix(raw, "kg_live_") {
			_, ak, err := db.FindProductByAPIKey(c.Request.Context(), store.HashAPIKey(raw))
			if err != nil {
				abortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "invalid api_key")
				return
			}
			// Bump last_used + last_used_ip. Fire-and-forget; an
			// occasional missed bump is fine but a blocked request
			// would be terrible.
			db.TouchAPIKey(ak.ID, c.ClientIP())

			// Hand the request through with the same identity-context
			// shape an admin session uses so downstream code (audit
			// logs, handler helpers) doesn't need to special-case.
			//
			// is_admin is left UNSET here — RequireScope decides
			// whether the key can hit this specific route. Setting it
			// would let any valid kg_live_ token through AdminOnly().
			c.Set("user_id", "apikey:"+ak.ID)
			c.Set("email", "apikey:"+ak.ID)
			c.Set("name", ak.Name)
			c.Set("api_key", ak)
			c.Set("auth_type", "api_key")
			c.Next()
			return
		}
		// Not an api_key — delegate to JWT/cookie auth, then tag the
		// session so RequireScope can recognise it.
		c.Set("auth_type", "session")
		sessionMW(c)
	}
}

func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Verify SessionAuth ran first
		if _, exists := c.Get("user_id"); !exists {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}
		v, _ := c.Get("is_admin")
		if v != true {
			abortWithError(c, http.StatusForbidden, "FORBIDDEN", "admin required")
			return
		}
		c.Next()
	}
}

// RequireScope is the single source of truth for "may this caller
// touch this route?" Behavior:
//
//   - Session-authenticated admin → always pass. An interactive admin
//     login has all powers; restricting it via API-style scopes would
//     break the dashboard.
//
//   - API key with `admin` scope → always pass. admin is the wildcard.
//
//   - API key with at least one of the listed `allowed` scopes → pass.
//
//   - Anything else (no auth, unknown scope, expired session, etc.)
//     → 403 INSUFFICIENT_SCOPE.
//
// Callers list every scope that should reach this route. Passing
// only `model.ScopeAdmin` means "admin-only" (most routes). Passing
// `model.ScopeAdmin, model.ScopeLicensesWrite` means a license-write
// key is also allowed, in addition to admins.
func RequireScope(allowed ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.GetString("auth_type") {
		case "session":
			if v, _ := c.Get("is_admin"); v == true {
				c.Next()
				return
			}
			abortWithError(c, http.StatusForbidden, "FORBIDDEN", "admin required")
			return
		case "api_key":
			v, ok := c.Get("api_key")
			if !ok {
				abortWithError(c, http.StatusForbidden, "FORBIDDEN", "missing api key context")
				return
			}
			ak, ok := v.(*model.APIKey)
			if !ok {
				abortWithError(c, http.StatusForbidden, "FORBIDDEN", "missing api key context")
				return
			}
			// admin is the wildcard; otherwise any explicit match wins.
			if slices.Contains(ak.Scopes, model.ScopeAdmin) {
				c.Next()
				return
			}
			for _, want := range allowed {
				if slices.Contains(ak.Scopes, want) {
					c.Next()
					return
				}
			}
			abortWithError(c, http.StatusForbidden, "INSUFFICIENT_SCOPE",
				"api_key is missing a required scope")
		default:
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		}
	}
}
