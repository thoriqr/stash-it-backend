package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
)

const (
	verificationCodeLength = 6
	verificationCodeMax    = 1000000
)

type VerificationCodeHasher struct {
	secret []byte
}

func NewVerificationCodeHasher(secret []byte) *VerificationCodeHasher {
	return &VerificationCodeHasher{
		secret: secret,
	}
}

func (h *VerificationCodeHasher) Generate() (string, error) {
	n, err := rand.Int(
		rand.Reader,
		big.NewInt(verificationCodeMax),
	)
	if err != nil {
		return "", fmt.Errorf("generate verification code: %w", err)
	}

	return fmt.Sprintf("%0*d", verificationCodeLength, n.Int64()), nil
}

func (h *VerificationCodeHasher) Hash(code string) string {
	mac := hmac.New(sha256.New, h.secret)

	_, _ = mac.Write([]byte(code))

	return hex.EncodeToString(mac.Sum(nil))
}

func (h *VerificationCodeHasher) Verify(code, encodedHash string) bool {
	expectedHash := h.Hash(code)

	return hmac.Equal(
		[]byte(expectedHash),
		[]byte(encodedHash),
	)
}