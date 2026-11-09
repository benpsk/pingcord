package githubhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("it's a secret to everybody")
	body := []byte(`{"hello":"world"}`)

	tests := []struct {
		name    string
		header  string
		wantErr bool
	}{
		{name: "valid", header: sign(secret, body)},
		{name: "empty header", header: "", wantErr: true},
		{name: "sha1 header", header: "sha1=abc", wantErr: true},
		{name: "not hex", header: "sha256=zzzz", wantErr: true},
		{name: "wrong secret", header: sign([]byte("other"), body), wantErr: true},
		{name: "tampered body", header: sign(secret, []byte(`{"hello":"w0rld"}`)), wantErr: true},
		{name: "truncated", header: sign(secret, body)[:20], wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySignature(secret, body, tt.header)
			if tt.wantErr && !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("err = %v, want ErrInvalidSignature", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Example from GitHub's docs: "Testing the webhook payload validation".
func TestVerifySignatureGitHubDocsVector(t *testing.T) {
	err := VerifySignature(
		[]byte("It's a Secret to Everybody"),
		[]byte("Hello, World!"),
		"sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17",
	)
	if err != nil {
		t.Fatalf("GitHub docs test vector failed: %v", err)
	}
}

func TestWellFormedSignature(t *testing.T) {
	valid := sign([]byte("k"), []byte("b"))
	for header, want := range map[string]bool{
		valid:                               true,
		"":                                  false,
		"sha1=" + valid[7:]:                 false,
		"sha256=" + valid[8:]:               false, // too short
		"sha256=" + valid[7:] + "00":        false, // too long
		"sha256=" + strings.Repeat("z", 64): false,
	} {
		if got := WellFormedSignature(header); got != want {
			t.Errorf("WellFormedSignature(%q) = %v, want %v", header, got, want)
		}
	}
}
