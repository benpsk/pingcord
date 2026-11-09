package githubhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrInvalidSignature is returned when a webhook signature is missing or wrong.
var ErrInvalidSignature = errors.New("invalid webhook signature")

const signaturePrefix = "sha256="

// VerifySignature checks the X-Hub-Signature-256 header against an
// HMAC-SHA256 of body using secret, in constant time.
func VerifySignature(secret, body []byte, header string) error {
	hexSig, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return ErrInvalidSignature
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}
