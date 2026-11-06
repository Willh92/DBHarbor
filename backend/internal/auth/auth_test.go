package auth

import (
	"strings"
	"testing"
	"time"
)

func TestJWTLifecycleAndPurposes(t *testing.T) {
	tm := NewTokenManager([]byte("0123456789abcdef0123456789abcdef"))

	sessTok, err := tm.IssueSessionToken("u1", "admin", "sid-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}
	claims, err := tm.ParseToken(sessTok)
	if err != nil || claims.UserID != "u1" || claims.SessionID != "sid-1" {
		t.Fatalf("unexpected session claims: %+v, err=%v", claims, err)
	}

	// Challenge token must NOT pass normal session ParseToken
	chalTok, err := tm.Issue2FAChallengeToken("u1", "admin", 5*time.Minute)
	if err != nil {
		t.Fatalf("Issue2FAChallengeToken: %v", err)
	}
	if _, err := tm.ParseToken(chalTok); err == nil {
		t.Fatal("expected 2fa_challenge token to be rejected as session token")
	}
	if _, err := tm.ParseTokenWithPurpose(chalTok, Purpose2FAChallenge); err != nil {
		t.Fatalf("expected valid 2fa_challenge token: %v", err)
	}

	// Single-use export ticket
	ticket, err := tm.IssueExportTicket("u1", "admin", "sid-1", time.Minute)
	if err != nil {
		t.Fatalf("IssueExportTicket: %v", err)
	}
	if _, err := tm.ParseTokenWithPurpose(ticket, PurposeExportTicket); err != nil {
		t.Fatalf("first ticket use should pass: %v", err)
	}
	if _, err := tm.ParseTokenWithPurpose(ticket, PurposeExportTicket); err == nil {
		t.Fatal("second ticket use should be rejected (single-use)")
	}

	// Session revocation
	tm.RevokeSessionInMemory("sid-1", 0)
	if _, err := tm.ParseToken(sessTok); err == nil {
		t.Fatal("revoked session should fail ParseToken")
	}
}

func TestTOTPAndAntiReplay(t *testing.T) {
	sec, err := GenerateTOTPSecret()
	if err != nil || len(sec) < 16 {
		t.Fatalf("GenerateTOTPSecret failed: %v", err)
	}
	now := time.Unix(1700000100, 0)
	code, step, err := GenerateTOTPCodeAt(sec, now)
	if err != nil || len(code) != 6 {
		t.Fatalf("GenerateTOTPCodeAt failed: %v", err)
	}

	matchedStep, ok := VerifyTOTP(sec, code, now, 0)
	if !ok || matchedStep != step {
		t.Fatalf("VerifyTOTP expected ok at step %d, got %d %v", step, matchedStep, ok)
	}

	// Replay in same/earlier step must fail
	if _, ok := VerifyTOTP(sec, code, now, matchedStep); ok {
		t.Fatal("VerifyTOTP should reject replayed step")
	}

	// Next 30s step should pass
	codeNext, _, _ := GenerateTOTPCodeAt(sec, now.Add(30*time.Second))
	if _, ok := VerifyTOTP(sec, codeNext, now.Add(30*time.Second), matchedStep); !ok {
		t.Fatal("VerifyTOTP should accept subsequent step")
	}
}

func TestOfflineQRCodeSVG(t *testing.T) {
	uri := BuildOTPAuthURI("DBHarbor", "admin", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
	dataURL, err := GenerateQRCodeDataURL(uri)
	if err != nil {
		t.Fatalf("GenerateQRCodeDataURL failed: %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:image/svg+xml;base64,") {
		t.Fatalf("unexpected dataURL prefix: %s", dataURL[:30])
	}
}

func TestLoginProtectorRateLimitAndLockout(t *testing.T) {
	lp := NewLoginProtector([]byte("secret-key-for-captcha"))
	ip := "192.168.1.50"

	for i := 1; i <= 5; i++ {
		st := lp.RecordFailure(ip, "admin")
		if i == 3 && !st.RequiresCaptcha {
			t.Fatal("expected captcha requirement at 3rd failure")
		}
		if i == 5 && !st.Locked {
			t.Fatal("expected lockout at 5th failure")
		}
	}
	chk := lp.Check(ip, "admin")
	if !chk.Locked || chk.RetryAfterSec <= 0 {
		t.Fatalf("expected locked status, got %+v", chk)
	}
	lp.RecordSuccess(ip, "admin")
	if lp.Check(ip, "admin").Locked {
		t.Fatal("expected lockout cleared after RecordSuccess")
	}
}
