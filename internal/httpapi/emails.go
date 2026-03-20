package httpapi

import (
	"regexp"
	"strings"
)

var basicEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isValidEmailSyntax(email string) bool {
	if email == "" || len(email) > accountEmailMaxBytes {
		return false
	}
	return basicEmailPattern.MatchString(email)
}
