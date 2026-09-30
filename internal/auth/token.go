// Package auth produces the bearer token the emulator attaches to outgoing
// task and scheduler requests, so targets that verify a service-account JWT
// (like the StarterByte backend) accept them.
//
// Configuration is read from the environment on every call:
//
//	SERVICE_ACCOUNT_TOKEN       static token, sent as-is
//	SERVICE_ACCOUNT_JWT_SECRET  HS256 secret; a fresh 1h token is minted per request
//	SERVICE_ACCOUNT_EMAIL       "email" claim when minting (default local-dev@example.com)
//	SERVICE_ACCOUNT_AUDIENCE    "aud" claim when minting (default authenticated)
//
// SERVICE_ACCOUNT_TOKEN wins if both are set. With neither set, no header is added.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

const tokenTTL = time.Hour

// Bearer returns the token to send, or "" when auth is not configured.
func Bearer() string {
	if t := os.Getenv("SERVICE_ACCOUNT_TOKEN"); t != "" {
		return t
	}
	if secret := os.Getenv("SERVICE_ACCOUNT_JWT_SECRET"); secret != "" {
		return mint(secret, time.Now())
	}
	return ""
}

// Apply sets the Authorization header on req unless the task or job already
// supplied its own.
func Apply(req *http.Request) {
	if req.Header.Get("Authorization") != "" {
		return
	}
	if t := Bearer(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
}

func mint(secret string, now time.Time) string {
	email := os.Getenv("SERVICE_ACCOUNT_EMAIL")
	if email == "" {
		email = "local-dev@example.com"
	}
	aud := os.Getenv("SERVICE_ACCOUNT_AUDIENCE")
	if aud == "" {
		aud = "authenticated"
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"sub":   "service-account",
		"role":  "service_role",
		"aud":   aud,
		"iat":   now.Unix(),
		"exp":   now.Add(tokenTTL).Unix(),
		"email": email,
	})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + enc.EncodeToString(mac.Sum(nil))
}
