package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/pkg/response"
)

// OTPSend handles POST /api/v1/auth/otp/send
func (h *AuthHandler) OTPSend(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "valid email is required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Rate limit: max 3 OTP requests per email per 10 minutes
	count, err := h.Store.CountRecentOTPCodes(c, email)
	if err != nil {
		response.Internal(c)
		return
	}
	if count >= 3 {
		response.Err(c, 429, "RATE_LIMITED", "too many code requests, try again later")
		return
	}

	// Signup gate: with OTP_REQUIRE_EXISTING_USER set, codes only go to
	// addresses that already have an account (ADMIN_EMAILS bypass covers
	// first-admin bootstrap). Unknown emails get the same "sent" response
	// with no code created, so the endpoint can't confirm which addresses
	// have accounts — and can't be used to spam arbitrary inboxes.
	if h.Config.OTPRequireExistingUser && !h.Config.IsAdminEmail(email) {
		if _, err := h.Store.FindUserByEmail(c, email); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				response.Internal(c)
				return
			}
			slog.Info("OTP requested for unknown email — not sent (OTP_REQUIRE_EXISTING_USER)", "email", email)
			response.OK(c, gin.H{"status": "sent"})
			return
		}
	}

	code := generateOTPCode()
	codeHash := hashOTPCode(code)

	otp := &model.OTPCode{
		Email:     email,
		CodeHash:  codeHash,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := h.Store.CreateOTPCode(c, otp); err != nil {
		response.Internal(c)
		return
	}

	if h.Email != nil && h.Email.IsConfigured() {
		h.Email.SendOTPCode(email, code)
	} else {
		slog.Warn("SMTP not configured — OTP code printed to log (configure SMTP for email delivery)",
			"email", email, "code", code)
	}

	response.OK(c, gin.H{"status": "sent"})
}

// OTPVerify handles POST /api/v1/auth/otp/verify
func (h *AuthHandler) OTPVerify(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "email and code are required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	code := strings.TrimSpace(req.Code)

	otp, err := h.Store.FindLatestValidOTPCode(c, email)

	// Always perform hash comparison to prevent timing-based email enumeration
	expectedHash := hashOTPCode("") // dummy
	otpID := ""
	otpAttempts := 0
	if err == nil && otp != nil {
		expectedHash = otp.CodeHash
		otpID = otp.ID
		otpAttempts = otp.Attempts
	}

	codeMatch := hmac.Equal([]byte(hashOTPCode(code)), []byte(expectedHash))

	if otpID != "" {
		if err := h.Store.IncrementOTPAttempts(c, otpID); err != nil {
			slog.Warn("failed to increment OTP attempts", "id", otpID, "error", err)
		}
	}

	if !codeMatch || otp == nil {
		remaining := 5 - (otpAttempts + 1)
		if remaining <= 0 {
			response.Unauthorized(c, "too many attempts, request a new code")
		} else {
			response.Unauthorized(c, "invalid or expired code")
		}
		return
	}

	if err := h.Store.MarkOTPUsed(c, otpID); err != nil {
		slog.Warn("failed to mark OTP used", "id", otpID, "error", err)
	}

	// Upsert user (create on first login)
	user := &model.User{Email: email}
	if err := h.Store.UpsertUser(c, user); err != nil {
		response.Internal(c)
		return
	}
	user, err = h.Store.FindUserByEmail(c, email)
	if err != nil {
		response.Internal(c)
		return
	}

	// Auto-promote if email is in ADMIN_EMAILS
	if h.Config.IsAdminEmail(user.Email) && user.Role == model.RoleUser {
		_ = h.Store.SetUserRole(c, user.ID, model.RoleAdmin)
		user.Role = model.RoleAdmin
	}

	// Welcome email for new users
	if h.Email != nil && time.Since(user.CreatedAt) < time.Minute {
		h.Email.SendWelcome(user.Email, user.Name)
	}

	h.issueSession(c, user)

	h.Store.Audit(c, &model.AuditLog{
		Entity: "session", EntityID: user.ID, Action: "login",
		ActorType: "otp", ActorID: user.ID, IPAddress: c.ClientIP(),
		Changes: map[string]any{"email": user.Email},
	})

	response.OK(c, gin.H{
		"status": "ok", "email": user.Email, "name": user.Name,
		"is_admin": user.IsAdmin(), "role": user.Role,
	})
}

func generateOTPCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

func hashOTPCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}
