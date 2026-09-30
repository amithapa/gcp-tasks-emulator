package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNoConfigNoHeader(t *testing.T) {
	t.Setenv("SERVICE_ACCOUNT_TOKEN", "")
	t.Setenv("SERVICE_ACCOUNT_JWT_SECRET", "")
	req, _ := http.NewRequest("GET", "http://x", nil)
	Apply(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}

func TestStaticTokenWinsAndExplicitHeaderIsKept(t *testing.T) {
	t.Setenv("SERVICE_ACCOUNT_TOKEN", "static")
	t.Setenv("SERVICE_ACCOUNT_JWT_SECRET", "secret")
	req, _ := http.NewRequest("GET", "http://x", nil)
	Apply(req)
	if got := req.Header.Get("Authorization"); got != "Bearer static" {
		t.Errorf("Authorization = %q", got)
	}
	req.Header.Set("Authorization", "Bearer mine")
	Apply(req)
	if got := req.Header.Get("Authorization"); got != "Bearer mine" {
		t.Errorf("explicit header overwritten: %q", got)
	}
}

func TestMintedTokenIsValidHS256(t *testing.T) {
	t.Setenv("SERVICE_ACCOUNT_TOKEN", "")
	t.Setenv("SERVICE_ACCOUNT_JWT_SECRET", "s3cret")
	t.Setenv("SERVICE_ACCOUNT_EMAIL", "")
	t.Setenv("SERVICE_ACCOUNT_AUDIENCE", "")

	parts := strings.Split(Bearer(), ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); parts[2] != want {
		t.Error("signature mismatch")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c["sub"] != "service-account" || c["role"] != "service_role" || c["aud"] != "authenticated" || c["email"] != "local-dev@example.com" {
		t.Errorf("claims = %v", c)
	}
	if exp, iat := c["exp"].(float64), c["iat"].(float64); time.Duration(exp-iat)*time.Second != time.Hour {
		t.Errorf("ttl = %vs", exp-iat)
	}
}
