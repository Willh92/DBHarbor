package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	PurposeSession      = "session"
	Purpose2FAChallenge = "2fa_challenge"
	PurposeExportTicket = "export_ticket"
)

var jwtHeaderB64 = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

type Claims struct {
	UserID    string `json:"uid"`
	Username  string `json:"username"`
	SessionID string `json:"sid,omitempty"`
	Nonce     string `json:"jti,omitempty"`
	Purpose   string `json:"purpose,omitempty"`
	Issuer    string `json:"iss,omitempty"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

func randomJTI() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type SessionValidator func(sessionID string) bool

type TokenManager struct {
	secret           []byte
	mu               sync.RWMutex
	revokedSessions  map[string]int64 // sid -> exp unix
	usedTickets      map[string]int64 // ticket sig -> exp unix
	challengeFails   map[string]int   // 2fa challenge sig -> failed attempts
	sessionValidator SessionValidator
}

func NewTokenManager(secret []byte) *TokenManager {
	cp := make([]byte, len(secret))
	copy(cp, secret)
	return &TokenManager{
		secret:          cp,
		revokedSessions: make(map[string]int64),
		usedTickets:     make(map[string]int64),
		challengeFails:  make(map[string]int),
	}
}

// Record2FAChallengeAttempt tracks failed 2FA attempts on a challenge token and consumes it on success or after 5 failures.
func (tm *TokenManager) Record2FAChallengeAttempt(tokenStr string, succeeded bool) (exhausted bool) {
	parts := strings.Split(strings.TrimSpace(tokenStr), ".")
	if len(parts) != 3 {
		return true
	}
	sig := parts[2]
	nowUnix := time.Now().Unix()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.pruneLocked(nowUnix)
	if _, used := tm.usedTickets[sig]; used {
		return true
	}
	if succeeded {
		tm.usedTickets[sig] = nowUnix + 600
		delete(tm.challengeFails, sig)
		return false
	}
	tm.challengeFails[sig]++
	if tm.challengeFails[sig] >= 5 {
		tm.usedTickets[sig] = nowUnix + 600
		delete(tm.challengeFails, sig)
		return true
	}
	if len(tm.challengeFails) > 2048 {
		for k := range tm.challengeFails {
			delete(tm.challengeFails, k)
			if len(tm.challengeFails) <= 1024 {
				break
			}
		}
	}
	return false
}

// Is2FAChallengeConsumed returns true if the 2FA challenge token has already been used or exhausted.
func (tm *TokenManager) Is2FAChallengeConsumed(tokenStr string) bool {
	parts := strings.Split(strings.TrimSpace(tokenStr), ".")
	if len(parts) != 3 {
		return true
	}
	tm.mu.RLock()
	_, used := tm.usedTickets[parts[2]]
	tm.mu.RUnlock()
	return used
}

func (tm *TokenManager) SetSessionValidator(fn SessionValidator) {
	tm.mu.Lock()
	tm.sessionValidator = fn
	tm.mu.Unlock()
}

func (tm *TokenManager) RevokeSessionInMemory(sessionID string, expUnix int64) {
	if sessionID == "" {
		return
	}
	if expUnix <= 0 {
		expUnix = time.Now().Add(8 * 24 * time.Hour).Unix()
	}
	tm.mu.Lock()
	tm.pruneLocked(time.Now().Unix())
	tm.revokedSessions[sessionID] = expUnix
	tm.mu.Unlock()
}

func (tm *TokenManager) pruneLocked(nowUnix int64) {
	if len(tm.revokedSessions) > 512 {
		for k, exp := range tm.revokedSessions {
			if exp < nowUnix {
				delete(tm.revokedSessions, k)
			}
		}
	}
	if len(tm.usedTickets) > 512 {
		for k, exp := range tm.usedTickets {
			if exp < nowUnix {
				delete(tm.usedTickets, k)
			}
		}
	}
}

// IssueToken issues a standard session JWT (compatible helper).
func (tm *TokenManager) IssueToken(userID, username string, ttl time.Duration) (string, error) {
	return tm.IssueSessionToken(userID, username, "", ttl)
}

func (tm *TokenManager) IssueSessionToken(userID, username, sessionID string, ttl time.Duration) (string, error) {
	return tm.signClaims(Claims{
		UserID:    userID,
		Username:  username,
		SessionID: sessionID,
		Purpose:   PurposeSession,
		Issuer:    "dbharbor",
	}, ttl)
}

func (tm *TokenManager) Issue2FAChallengeToken(userID, username string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return tm.signClaims(Claims{
		UserID:   userID,
		Username: username,
		Nonce:    randomJTI(),
		Purpose:  Purpose2FAChallenge,
		Issuer:   "dbharbor",
	}, ttl)
}

func (tm *TokenManager) IssueExportTicket(userID, username, sessionID string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return tm.signClaims(Claims{
		UserID:    userID,
		Username:  username,
		SessionID: sessionID,
		Nonce:     randomJTI(),
		Purpose:   PurposeExportTicket,
		Issuer:    "dbharbor",
	}, ttl)
}

func (tm *TokenManager) signClaims(c Claims, ttl time.Duration) (string, error) {
	now := time.Now().Unix()
	c.IssuedAt = now
	c.ExpiresAt = now + int64(ttl.Seconds())
	if c.ExpiresAt <= now {
		c.ExpiresAt = now + 60
	}
	payloadBytes, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	unsigned := jwtHeaderB64 + "." + base64.RawURLEncoding.EncodeToString(payloadBytes)
	mac := hmac.New(sha256.New, tm.secret)
	_, _ = mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + sig, nil
}

// ParseToken verifies signature and expiration, checking purpose == PurposeSession by default.
func (tm *TokenManager) ParseToken(tokenStr string) (*Claims, error) {
	return tm.ParseTokenWithPurpose(tokenStr, PurposeSession)
}

func (tm *TokenManager) ParseTokenWithPurpose(tokenStr, expectedPurpose string) (*Claims, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	unsigned := parts[0] + "." + parts[1]
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("invalid token signature encoding")
	}

	mac := hmac.New(sha256.New, tm.secret)
	_, _ = mac.Write([]byte(unsigned))
	expectedSig := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expectedSig) {
		return nil, errors.New("invalid token signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid token payload")
	}
	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, errors.New("invalid token claims")
	}

	now := time.Now().Unix()
	if claims.ExpiresAt <= now {
		return nil, errors.New("authentication token has expired")
	}

	purpose := claims.Purpose
	if purpose == "" {
		purpose = PurposeSession
	}
	if expectedPurpose != "" && purpose != expectedPurpose {
		return nil, errors.New("token purpose mismatch")
	}

	// Single-use enforcement for export tickets
	if purpose == PurposeExportTicket {
		tm.mu.Lock()
		if _, used := tm.usedTickets[parts[2]]; used {
			tm.mu.Unlock()
			return nil, errors.New("single-use export ticket has already been used or expired")
		}
		tm.pruneLocked(now)
		tm.usedTickets[parts[2]] = claims.ExpiresAt
		tm.mu.Unlock()
	}

	// Revocation check for session-bound tokens
	if claims.SessionID != "" {
		tm.mu.RLock()
		_, revoked := tm.revokedSessions[claims.SessionID]
		validator := tm.sessionValidator
		tm.mu.RUnlock()
		if revoked {
			return nil, errors.New("session has been revoked; please sign in again")
		}
		if validator != nil && !validator(claims.SessionID) {
			return nil, errors.New("session is no longer active; please sign in again")
		}
	}

	return &claims, nil
}
