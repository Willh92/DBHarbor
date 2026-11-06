package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	captchaThreshold = 3
	maxFailAttempts  = 5
	failWindow       = 10 * time.Minute
	lockoutDuration  = 15 * time.Minute
	captchaAlphabet  = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
)

type attemptRecord struct {
	failures    int
	firstFailAt time.Time
	lockedUntil time.Time
}

// LoginProtector enforces brute-force rate limiting, progressive IP/account lockouts,
// and stateless HMAC-signed offline SVG captcha challenges.
type LoginProtector struct {
	secret []byte
	mu     sync.Mutex
	byKey  map[string]*attemptRecord
	byIP   map[string]*attemptRecord
}

type CheckStatus struct {
	Locked            bool
	RetryAfterSec     int
	RequiresCaptcha   bool
	RemainingAttempts int
}

func NewLoginProtector(secret []byte) *LoginProtector {
	cp := make([]byte, len(secret))
	copy(cp, secret)
	return &LoginProtector{
		secret: cp,
		byKey:  make(map[string]*attemptRecord),
		byIP:   make(map[string]*attemptRecord),
	}
}

func (lp *LoginProtector) Check(ip, username string) CheckStatus {
	now := time.Now()
	key := ip + "|" + strings.ToLower(strings.TrimSpace(username))

	lp.mu.Lock()
	defer lp.mu.Unlock()
	lp.pruneLocked(now)

	recKey := lp.getActiveLocked(lp.byKey, key, now)
	recIP := lp.getActiveLocked(lp.byIP, ip, now)

	var lockedUntil time.Time
	if recKey != nil && recKey.lockedUntil.After(now) {
		lockedUntil = recKey.lockedUntil
	}
	if recIP != nil && recIP.lockedUntil.After(lockedUntil) {
		lockedUntil = recIP.lockedUntil
	}
	if !lockedUntil.IsZero() {
		sec := int(lockedUntil.Sub(now).Seconds()) + 1
		return CheckStatus{Locked: true, RetryAfterSec: sec, RequiresCaptcha: true, RemainingAttempts: 0}
	}

	fails := 0
	if recKey != nil && recKey.failures > fails {
		fails = recKey.failures
	}
	ipFails := 0
	if recIP != nil {
		ipFails = recIP.failures
	}
	reqCaptcha := fails >= captchaThreshold || ipFails >= captchaThreshold
	rem := maxFailAttempts - fails
	if rem < 1 {
		rem = 1
	}
	return CheckStatus{
		Locked:            false,
		RequiresCaptcha:   reqCaptcha,
		RemainingAttempts: rem,
	}
}

func (lp *LoginProtector) RecordFailure(ip, username string) CheckStatus {
	now := time.Now()
	key := ip + "|" + strings.ToLower(strings.TrimSpace(username))

	lp.mu.Lock()
	lp.pruneLocked(now)
	rk := lp.bumpLocked(lp.byKey, key, now, maxFailAttempts)
	ri := lp.bumpLocked(lp.byIP, ip, now, maxFailAttempts*2)
	lp.mu.Unlock()

	var lockedUntil time.Time
	if rk.lockedUntil.After(now) {
		lockedUntil = rk.lockedUntil
	}
	if ri.lockedUntil.After(lockedUntil) {
		lockedUntil = ri.lockedUntil
	}
	if !lockedUntil.IsZero() {
		return CheckStatus{
			Locked:            true,
			RetryAfterSec:     int(lockedUntil.Sub(now).Seconds()) + 1,
			RequiresCaptcha:   true,
			RemainingAttempts: 0,
		}
	}
	rem := maxFailAttempts - rk.failures
	if rem < 0 {
		rem = 0
	}
	return CheckStatus{
		Locked:            false,
		RequiresCaptcha:   rk.failures >= captchaThreshold || ri.failures >= captchaThreshold,
		RemainingAttempts: rem,
	}
}

func (lp *LoginProtector) RecordSuccess(ip, username string) {
	key := ip + "|" + strings.ToLower(strings.TrimSpace(username))
	lp.mu.Lock()
	delete(lp.byKey, key)
	delete(lp.byIP, ip)
	lp.mu.Unlock()
}

func (lp *LoginProtector) getActiveLocked(m map[string]*attemptRecord, k string, now time.Time) *attemptRecord {
	r, ok := m[k]
	if !ok {
		return nil
	}
	if !r.lockedUntil.IsZero() && now.After(r.lockedUntil) {
		delete(m, k)
		return nil
	}
	if r.lockedUntil.IsZero() && now.Sub(r.firstFailAt) > failWindow {
		delete(m, k)
		return nil
	}
	return r
}

func (lp *LoginProtector) bumpLocked(m map[string]*attemptRecord, k string, now time.Time, threshold int) *attemptRecord {
	r := lp.getActiveLocked(m, k, now)
	if r == nil {
		r = &attemptRecord{firstFailAt: now}
		m[k] = r
	}
	r.failures++
	if r.failures >= threshold {
		r.lockedUntil = now.Add(lockoutDuration)
	}
	return r
}

func (lp *LoginProtector) pruneLocked(now time.Time) {
	if len(lp.byKey) > 1024 {
		for k, r := range lp.byKey {
			if (!r.lockedUntil.IsZero() && now.After(r.lockedUntil)) || (r.lockedUntil.IsZero() && now.Sub(r.firstFailAt) > failWindow) {
				delete(lp.byKey, k)
			}
		}
	}
	if len(lp.byIP) > 1024 {
		for k, r := range lp.byIP {
			if (!r.lockedUntil.IsZero() && now.After(r.lockedUntil)) || (r.lockedUntil.IsZero() && now.Sub(r.firstFailAt) > failWindow) {
				delete(lp.byIP, k)
			}
		}
	}
}

// GenerateCaptcha produces a stateless HMAC-signed challenge token and a distorted offline SVG image data URL.
func (lp *LoginProtector) GenerateCaptcha() (captchaToken string, imageDataURL string, err error) {
	var rnd [16]byte
	if _, err := io.ReadFull(rand.Reader, rnd[:]); err != nil {
		return "", "", err
	}
	var chars [5]byte
	for i := 0; i < 5; i++ {
		chars[i] = captchaAlphabet[int(rnd[i])%len(captchaAlphabet)]
	}
	code := string(chars[:])
	nonce := hex.EncodeToString(rnd[5:13])
	exp := time.Now().Add(3 * time.Minute).Unix()

	sig := lp.signCaptcha(nonce, code, exp)
	captchaToken = fmt.Sprintf("%s.%d.%s", nonce, exp, sig)

	// Render clean distorted SVG
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="132" height="40" viewBox="0 0 132 40">`)
	svg.WriteString(`<rect width="132" height="40" rx="6" fill="#f0f5ff"/>`)
	svg.WriteString(fmt.Sprintf(
		`<path d="M4,%d Q45,%d 88,%d T128,%d" fill="none" stroke="#91caff" stroke-width="2"/>`,
		10+int(rnd[13]%20), 5+int(rnd[14]%30), 8+int(rnd[15]%24), 12+int(rnd[0]%18),
	))
	svg.WriteString(fmt.Sprintf(
		`<path d="M6,%d Q64,%d 126,%d" fill="none" stroke="#b7eb8f" stroke-width="1.6"/>`,
		28-int(rnd[1]%16), 8+int(rnd[2]%24), 10+int(rnd[3]%22),
	))
	colors := []string{"#0958d9", "#08979c", "#531dab", "#d46b08", "#cf1322"}
	for i := 0; i < 5; i++ {
		x := 16 + i*22
		y := 26 + int(rnd[6+i]%7) - 3
		rot := int(rnd[11+i]%26) - 13
		svg.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" fill="%s" font-family="monospace" font-size="21" font-weight="bold" transform="rotate(%d %d %d)">%c</text>`,
			x, y, colors[i], rot, x, y, code[i],
		))
	}
	svg.WriteString(`</svg>`)

	imageDataURL = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg.String()))
	return captchaToken, imageDataURL, nil
}

func (lp *LoginProtector) VerifyCaptcha(captchaToken, answer string) bool {
	parts := strings.Split(strings.TrimSpace(captchaToken), ".")
	if len(parts) != 3 {
		return false
	}
	nonce := parts[0]
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	cleanAns := strings.ToUpper(strings.TrimSpace(answer))
	if len(cleanAns) != 5 {
		return false
	}
	expectedSig := lp.signCaptcha(nonce, cleanAns, exp)
	return hmac.Equal([]byte(parts[2]), []byte(expectedSig))
}

func (lp *LoginProtector) signCaptcha(nonce, code string, exp int64) string {
	mac := hmac.New(sha256.New, lp.secret)
	_, _ = fmt.Fprintf(mac, "captcha:%s:%s:%d", nonce, strings.ToUpper(code), exp)
	return hex.EncodeToString(mac.Sum(nil))
}
