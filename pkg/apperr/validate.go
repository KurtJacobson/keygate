package apperr

import (
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// semverShape: a permissive prefilter to short-circuit obvious garbage
// before golang.org/x/mod/semver does the strict 2.0 parse (which is
// what catches leading zeros, empty pre-release identifiers, etc.).
var semverShape = regexp.MustCompile(
	`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`,
)

// ValidateSemver returns nil iff value is a strict SemVer 2.0 version
// string. Leading zeros (e.g. `01.0.0`), empty pre-release identifiers,
// and other 2.0 violations are rejected. Empty input is rejected — call
// sites that allow "no version" must short-circuit before calling.
func ValidateSemver(value string) *AppError {
	if value == "" {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "version is required"}
	}
	if len(value) > 64 {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "version must be at most 64 characters"}
	}
	if !semverShape.MatchString(value) || !semver.IsValid("v"+value) {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "version must be SemVer 2.0 (e.g. 1.2.3 or 1.2.3-beta.1)"}
	}
	return nil
}

func ValidateEmail(email string) *AppError {
	if email == "" {
		return BadRequest("email is required")
	}
	if len(email) > 254 {
		return BadRequest("email is too long")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return BadRequest("invalid email format")
	}
	if !strings.Contains(addr.Address, "@") {
		return BadRequest("invalid email format")
	}
	return nil
}

func ValidateName(field, value string) *AppError {
	if value == "" {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: field + " is required"}
	}
	if len(value) > 200 {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: field + " must be at most 200 characters"}
	}
	return nil
}

func ValidateSlug(value string) *AppError {
	if value == "" {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "slug is required"}
	}
	if len(value) < 2 {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "slug must be at least 2 characters"}
	}
	if len(value) > 100 {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "slug must be at most 100 characters"}
	}
	if value[0] == '-' || value[len(value)-1] == '-' {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "slug must not start or end with a hyphen"}
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "slug must contain only lowercase letters, numbers, and hyphens (a-z, 0-9, -)"}
		}
	}
	return nil
}

func ValidateURL(value string) *AppError {
	if value == "" {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "url is required"}
	}
	if len(value) > 2048 {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "url is too long"}
	}
	if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		return &AppError{Status: 400, Code: "INVALID_INPUT", Message: "url must start with https:// or http://"}
	}
	return nil
}
