package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

var b32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

const recoveryAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// GenerateTOTPSecret generates a 160-bit (20-byte) RFC 6238 Base32 secret without padding.
func GenerateTOTPSecret() (string, error) {
	var raw [20]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	return b32NoPad.EncodeToString(raw[:]), nil
}

// BuildOTPAuthURI builds a standard otpauth:// URI compatible with Google/Microsoft Authenticator, 1Password, Authy.
func BuildOTPAuthURI(issuer, accountName, seedB32 string) string {
	if issuer == "" {
		issuer = "DBHarbor"
	}
	label := url.PathEscape(issuer + ":" + accountName)
	q := url.Values{}
	q.Set("secret", strings.ToUpper(strings.TrimSpace(seedB32)))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// GenerateTOTPCodeAt computes the 6-digit RFC 6238 TOTP code and 30-second step counter for a given timestamp.
func GenerateTOTPCodeAt(seedB32 string, t time.Time) (string, int64, error) {
	clean := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(seedB32), " ", ""))
	clean = strings.TrimRight(clean, "=")
	key, err := b32NoPad.DecodeString(clean)
	if err != nil {
		return "", 0, err
	}
	step := t.Unix() / 30
	return hotpCode(key, uint64(step)), step, nil
}

func hotpCode(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binCode := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	otp := binCode % 1_000_000
	return fmt.Sprintf("%06d", otp)
}

// VerifyTOTP checks a 6-digit TOTP code with ±1 window tolerance and anti-replay step protection.
func VerifyTOTP(seedB32, inputCode string, now time.Time, lastUsedStep int64) (matchedStep int64, ok bool) {
	code := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(inputCode), " ", ""), "-", "")
	if len(code) != 6 {
		return 0, false
	}
	for i := 0; i < 6; i++ {
		if code[i] < '0' || code[i] > '9' {
			return 0, false
		}
	}

	clean := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(seedB32), " ", ""))
	clean = strings.TrimRight(clean, "=")
	key, err := b32NoPad.DecodeString(clean)
	if err != nil {
		return 0, false
	}

	baseStep := now.Unix() / 30
	for delta := int64(-1); delta <= 1; delta++ {
		candidateStep := baseStep + delta
		if lastUsedStep > 0 && candidateStep <= lastUsedStep {
			continue
		}
		expected := hotpCode(key, uint64(candidateStep))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return candidateStep, true
		}
	}
	return 0, false
}

// GenerateRecoveryCodes generates human-friendly XXXX-XXXX single-use backup codes and their SHA-256 hashes.
func GenerateRecoveryCodes(count int) (plainCodes []string, hashedCodes []string, err error) {
	if count <= 0 {
		count = 8
	}
	plainCodes = make([]string, count)
	hashedCodes = make([]string, count)
	var rnd [8]byte
	for i := 0; i < count; i++ {
		if _, err := io.ReadFull(rand.Reader, rnd[:]); err != nil {
			return nil, nil, err
		}
		var chars [9]byte
		for j := 0; j < 4; j++ {
			chars[j] = recoveryAlphabet[int(rnd[j])%len(recoveryAlphabet)]
		}
		chars[4] = '-'
		for j := 0; j < 4; j++ {
			chars[5+j] = recoveryAlphabet[int(rnd[4+j])%len(recoveryAlphabet)]
		}
		code := string(chars[:])
		plainCodes[i] = code
		hashedCodes[i] = HashRecoveryCode(code)
	}
	return plainCodes, hashedCodes, nil
}

// HashRecoveryCode normalizes and SHA-256 hashes a backup recovery code.
func HashRecoveryCode(code string) string {
	norm := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
	sum := sha256.Sum256([]byte("dbharbor-recovery:" + norm))
	return hex.EncodeToString(sum[:])
}
