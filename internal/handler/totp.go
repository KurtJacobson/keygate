package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/middleware"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/totp"
	"github.com/tabloy/keygate/pkg/response"
)

// pending2FATTL bounds how long a login may sit between the email-OTP
// step and the TOTP step.
const pending2FATTL = 5 * time.Minute

// checkTOTP validates a submitted code against the user's secret and
// claims the matched time-slot so the same code can't be used twice.
func (h *AuthHandler) checkTOTP(c *gin.Context, user *model.User, code string) bool {
	slot, ok := totp.Validate(user.TOTPSecret, code, time.Now().Unix())
	if !ok {
		return false
	}
	claimed, err := h.Store.ClaimTOTPSlot(c, user.ID, slot)
	return err == nil && claimed
}

// TOTPLogin handles POST /api/v1/auth/totp/verify — the second login
// step for accounts with TOTP enabled. Exchange: pending token (from
// OTP verify) + authenticator code → real session.
func (h *AuthHandler) TOTPLogin(c *gin.Context) {
	var req struct {
		PendingToken string `json:"pending_token" binding:"required"`
		Code         string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "pending_token and code are required")
		return
	}

	userID, err := middleware.ParsePending2FAJWT(h.Config.JWTSecret, req.PendingToken)
	if err != nil {
		response.Unauthorized(c, "invalid or expired login, start over")
		return
	}
	user, err := h.Store.FindUserByID(c, userID)
	if err != nil || !user.TOTPEnabled {
		response.Unauthorized(c, "invalid or expired login, start over")
		return
	}

	if !h.checkTOTP(c, user, req.Code) {
		response.Unauthorized(c, "invalid code")
		return
	}

	h.issueSession(c, user)
	h.Store.Audit(c, &model.AuditLog{
		Entity: "session", EntityID: user.ID, Action: "login",
		ActorType: "otp+totp", ActorID: user.ID, IPAddress: c.ClientIP(),
		Changes: map[string]any{"email": user.Email},
	})
	response.OK(c, gin.H{
		"status": "ok", "email": user.Email, "name": user.Name,
		"is_admin": user.IsAdmin(), "role": user.Role,
	})
}

// TOTPSetup handles POST /api/v1/portal/2fa/totp/setup. Generates a
// fresh secret in the pending state and returns it with the otpauth://
// URI for the QR code. Restarting setup regenerates; an active secret
// must be disabled first.
func (h *AuthHandler) TOTPSetup(c *gin.Context) {
	user, ok := h.sessionUser(c)
	if !ok {
		return
	}
	if user.TOTPEnabled {
		response.BadRequest(c, "two-factor auth is already enabled — disable it first")
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		response.Internal(c)
		return
	}
	if err := h.Store.SetPendingTOTPSecret(c, user.ID, secret); err != nil {
		response.Internal(c)
		return
	}
	response.OK(c, gin.H{
		"secret":      secret,
		"otpauth_uri": totp.ProvisioningURI("Keygate", user.Email, secret),
	})
}

// TOTPActivate handles POST /api/v1/portal/2fa/totp/activate — confirms
// the pending secret with a first valid code and turns enforcement on.
func (h *AuthHandler) TOTPActivate(c *gin.Context) {
	user, ok := h.sessionUser(c)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "code is required")
		return
	}
	if user.TOTPEnabled {
		response.BadRequest(c, "two-factor auth is already enabled")
		return
	}
	if user.TOTPSecret == "" {
		response.BadRequest(c, "no pending setup — call setup first")
		return
	}
	if !h.checkTOTP(c, user, req.Code) {
		response.Unauthorized(c, "invalid code")
		return
	}
	if err := h.Store.EnableTOTP(c, user.ID); err != nil {
		response.Internal(c)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "user", EntityID: user.ID, Action: "totp_enabled",
		ActorType: "user", ActorID: user.ID, IPAddress: c.ClientIP(),
	})
	response.OK(c, gin.H{"status": "enabled"})
}

// TOTPDisable handles POST /api/v1/portal/2fa/totp/disable. Requires a
// current code — a hijacked session alone must not be able to strip 2FA.
func (h *AuthHandler) TOTPDisable(c *gin.Context) {
	user, ok := h.sessionUser(c)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "code is required")
		return
	}
	if !user.TOTPEnabled {
		response.BadRequest(c, "two-factor auth is not enabled")
		return
	}
	if !h.checkTOTP(c, user, req.Code) {
		response.Unauthorized(c, "invalid code")
		return
	}
	if err := h.Store.DisableTOTP(c, user.ID); err != nil {
		response.Internal(c)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "user", EntityID: user.ID, Action: "totp_disabled",
		ActorType: "user", ActorID: user.ID, IPAddress: c.ClientIP(),
	})
	response.OK(c, gin.H{"status": "disabled"})
}

// sessionUser loads the full user row for the authenticated session.
// Writes the error response itself when the session context is broken.
func (h *AuthHandler) sessionUser(c *gin.Context) (*model.User, bool) {
	uid, _ := c.Get("user_id")
	id, ok := uid.(string)
	if !ok || id == "" || strings.HasPrefix(id, "apikey:") {
		response.Unauthorized(c, "unauthorized")
		return nil, false
	}
	user, err := h.Store.FindUserByID(c, id)
	if err != nil {
		response.Unauthorized(c, "unauthorized")
		return nil, false
	}
	return user, true
}
