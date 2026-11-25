// Package auth is passwordless sign-in: a one-time code sent by text message, then a session.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"launchpad/internal/apperr"
	"launchpad/internal/db"
	"launchpad/internal/services"
	"launchpad/internal/sms"
	"launchpad/internal/users"
)

const (
	OTPMinutes       = 10
	MaxGuesses       = 5
	ResendSeconds    = 30
	MaxSendsPerHour  = 10
	codeDigits       = 6
	otpKey           = "OTP#owner" // there is one phone, so one code at a time
	otpSort          = "OTP"
	sessionSort      = "SESSION"
	notSentLogLine   = "Sign-in code not sent"
	notSentMetric    = "SignInNotSent"
	invalidCodeText  = "That code is wrong or has expired"
	sessionExpiredAt = "Your session has expired. Sign in again"
)

// otp is the stored state of the current code. Only a salted hash of the code is kept.
type otp struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Salt      string `dynamodbav:"salt"`
	Hash      string `dynamodbav:"hash"`
	Attempts  int64  `dynamodbav:"attempts"`
	SentAt    int64  `dynamodbav:"sentAt"`
	HourStart int64  `dynamodbav:"hourStart"`
	HourCount int64  `dynamodbav:"hourCount"`
	TTL       int64  `dynamodbav:"ttl"`
}

type session struct {
	PK    string `dynamodbav:"pk"`
	SK    string `dynamodbav:"sk"`
	Email string `dynamodbav:"email"`
	TTL   int64  `dynamodbav:"ttl"`
}

func otpHash(salt, code string) (string, error) {
	key, err := hex.DecodeString(salt)
	if err != nil {
		return "", fmt.Errorf("the stored salt is not hex: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Start texts a one-time code to the owner's phone, if rawNumber is that phone.
//
// The caller learns nothing either way. A number that is not the owner's, a
// number that is, a send held back by a limit, a secret that is not set up and
// a failed send all end the same, so the answer cannot be used to find the
// number. The reason is logged and counted. Only a number that cannot be a
// phone number at all is refused, which says nothing about the right one.
func Start(ctx context.Context, svc *services.Services, rawNumber any) error {
	given, err := sms.NormaliseNumber(rawNumber)
	if err != nil {
		return err
	}
	if err := safeSendToOwner(ctx, svc, given); err != nil {
		reason := "error"
		if appErr, ok := apperr.As(err); ok {
			reason = appErr.Code
			svc.Log.Warn(notSentLogLine, "reason", reason)
		} else {
			// A failure of our own must look the same as everything else to the caller.
			svc.Log.Error(notSentLogLine, "reason", reason, "error", err)
		}
		svc.Metrics.Count(notSentMetric, "reason", reason)
	}
	return nil
}

// safeSendToOwner is sendToOwner with a panic turned into an error, so that a crash ends like
// every other reason for not sending and cannot be told apart from them.
func safeSendToOwner(ctx context.Context, svc *services.Services, given string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return sendToOwner(ctx, svc, given)
}

func sendToOwner(ctx context.Context, svc *services.Services, given string) error {
	owner, err := sms.SignInNumber(ctx, svc)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(owner), []byte(given)) != 1 {
		return apperr.New(403, "number_mismatch", "The number is not the owner's")
	}

	now := svc.Now()
	previous, err := db.GetAs[otp](ctx, svc.DB, otpKey, otpSort)
	if err != nil {
		return err
	}
	hourStart, hourCount := now, int64(0)
	if previous != nil {
		if now-previous.SentAt < ResendSeconds {
			return apperr.New(429, "too_soon", fmt.Sprintf("Wait %d seconds before asking for another code", ResendSeconds))
		}
		hourStart, hourCount = previous.HourStart, previous.HourCount
		if now-hourStart >= 3600 {
			hourStart, hourCount = now, 0
		}
	}
	if hourCount >= MaxSendsPerHour {
		return apperr.New(429, "too_many", "Too many codes requested. Try again later")
	}

	code, err := newCode()
	if err != nil {
		return err
	}
	salt, err := randomHex(16)
	if err != nil {
		return err
	}
	hash, err := otpHash(salt, code)
	if err != nil {
		return err
	}
	err = db.PutAs(ctx, svc.DB, otp{
		PK: otpKey, SK: otpSort, Salt: salt, Hash: hash, Attempts: 0,
		SentAt: now, HourStart: hourStart, HourCount: hourCount + 1, TTL: now + OTPMinutes*60,
	}, nil)
	if err != nil {
		return err
	}
	if err := sms.SendCode(ctx, svc, owner, code, OTPMinutes); err != nil {
		return err
	}
	svc.Metrics.Count("SignInStarted")
	return nil
}

// Result is a successful sign-in.
type Result struct {
	User  *users.User
	Token string
}

// Verify checks the code and returns the owner, signed in.
func Verify(ctx context.Context, svc *services.Services, rawCode any) (*Result, error) {
	invalid := apperr.BadRequest("invalid_code", invalidCodeText)
	code, _ := rawCode.(string)
	code = strings.TrimSpace(code)
	if !isSixDigits(code) {
		return nil, invalid
	}

	item, err := db.GetAs[otp](ctx, svc.DB, otpKey, otpSort)
	if err != nil {
		return nil, err
	}
	if item == nil || item.TTL <= svc.Now() {
		svc.Metrics.Count("SignInFailed")
		return nil, invalid
	}
	// Count the guess before looking at it, so parallel guesses cannot beat the limit.
	item, err = db.UpdateAs[otp](ctx, svc.DB, otpKey, otpSort,
		db.Update{Add: map[string]int64{"attempts": 1}},
		db.Less("attempts", MaxGuesses))
	if errors.Is(err, db.ErrConditionFailed) {
		if err := svc.DB.Delete(ctx, otpKey, otpSort, nil); err != nil {
			return nil, err
		}
		svc.Metrics.Count("SignInFailed")
		return nil, invalid
	}
	if err != nil {
		return nil, err
	}
	want, err := otpHash(item.Salt, code)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal([]byte(item.Hash), []byte(want)) {
		svc.Metrics.Count("SignInFailed")
		return nil, invalid
	}
	if err := svc.DB.Delete(ctx, otpKey, otpSort, nil); err != nil {
		return nil, err
	}

	user, err := users.EnsureOwner(ctx, svc)
	if err != nil {
		return nil, err
	}
	svc.Metrics.Count("SignInSucceeded")
	token, err := newSession(ctx, svc, user.Email)
	if err != nil {
		return nil, err
	}
	return &Result{User: user, Token: token}, nil
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "SESSION#" + hex.EncodeToString(sum[:])
}

func newSession(ctx context.Context, svc *services.Services, email string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	err := db.PutAs(ctx, svc.DB, session{
		PK: sessionKey(token), SK: sessionSort, Email: email,
		TTL: svc.Now() + int64(svc.Settings.SessionHours)*3600,
	}, nil)
	if err != nil {
		return "", err
	}
	return token, nil
}

// SessionUser is the signed-in owner for a session token, or a 401.
func SessionUser(ctx context.Context, svc *services.Services, token string) (*users.User, error) {
	if token == "" {
		return nil, apperr.Unauthorized()
	}
	item, err := db.GetAs[session](ctx, svc.DB, sessionKey(token), sessionSort)
	if err != nil {
		return nil, err
	}
	if item == nil || item.TTL <= svc.Now() {
		return nil, apperr.Unauthorized(sessionExpiredAt)
	}
	user, err := users.Get(ctx, svc, item.Email)
	if err != nil {
		return nil, err
	}
	// Checked on every request, so changing SuperUserEmail ends the old owner's sessions.
	if user == nil || user.Email != svc.Settings.SuperUserEmail {
		return nil, apperr.Unauthorized(sessionExpiredAt)
	}
	return user, nil
}

// Logout ends a session. An unknown token is not an error.
func Logout(ctx context.Context, svc *services.Services, token string) error {
	if token == "" {
		return nil
	}
	return svc.DB.Delete(ctx, sessionKey(token), sessionSort, nil)
}

func isSixDigits(s string) bool {
	if len(s) != codeDigits {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("random code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
