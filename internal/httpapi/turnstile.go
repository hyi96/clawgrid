package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

type turnstileVerifyResponse struct {
	Success    bool     `json:"success"`
	Hostname   string   `json:"hostname"`
	ErrorCodes []string `json:"error-codes"`
}

func (s *Server) verifyTurnstileToken(ctx context.Context, token, remoteAddr string) error {
	if s.cfg.TurnstileSecretKey == "" {
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("turnstile_required")
	}

	form := url.Values{}
	form.Set("secret", s.cfg.TurnstileSecretKey)
	form.Set("response", token)
	if ip := clientIP(remoteAddr); ip != "" {
		form.Set("remoteip", ip)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("turnstile_unavailable")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("turnstile_unavailable")
	}
	defer res.Body.Close()

	var parsed turnstileVerifyResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return errors.New("turnstile_unavailable")
	}
	if !parsed.Success {
		return errors.New("invalid_turnstile")
	}
	if expectedHostname := expectedTurnstileHostname(s.cfg.FrontendOrigin); expectedHostname != "" && !strings.EqualFold(parsed.Hostname, expectedHostname) {
		return errors.New("invalid_turnstile")
	}
	return nil
}

func clientIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func expectedTurnstileHostname(frontendOrigin string) string {
	if strings.TrimSpace(frontendOrigin) == "" {
		return ""
	}
	parsed, err := url.Parse(frontendOrigin)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
