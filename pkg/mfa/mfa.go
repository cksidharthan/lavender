// Package mfa is the second step of signing in with an authenticator app: time based one time codes (TOTP, RFC 6238),
// and recovery codes for when the phone is lost.
package mfa

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

const (
	issuer        = "Lavender"
	period        = 30
	skew          = 1 // codes of the step before and the step after are taken, as clocks are never the same
	recoveryCount = 10
	recoveryChars = "abcdefghjkmnpqrstuvwxyz23456789" // no letters that look like numbers
	qrSize        = 240

	// MaxFailures wrong codes in FailureWindow, and the user has to start again
	MaxFailures   = 5
	FailureWindow = 5 * time.Minute

	// ChallengeTTL is how long the user has for the second step after the password
	ChallengeTTL = 5 * time.Minute

	secretField   = "totp_secret"
	enabledField  = "totp_enabled"
	recoveryField = "totp_recovery"
	lastStepField = "totp_last_step"
)

var (
	// ErrInvalid is a code that is not right. It is the same for every reason, so it tells nothing about the account.
	ErrInvalid = errors.New("the code is not right")
	// ErrLocked is too many wrong codes
	ErrLocked = errors.New("too many wrong codes, try again later")
)

// InvalidError is something the user did that can not be done, the message is meant to be shown to the user.
type InvalidError struct{ Message string }

func (e InvalidError) Error() string { return e.Message }

type Service struct {
	app     core.App
	key     Key
	now     func() time.Time
	limiter *limiter
}

func New(app core.App, key Key) *Service {
	return &Service{app: app, key: key, now: time.Now, limiter: &limiter{failures: make(map[string][]time.Time)}}
}

// Enabled reports whether signing in needs a code as well as the password.
func Enabled(user *core.Record) bool {
	return user.GetBool(enabledField)
}

// Status tells whether the user has two factor authentication on, and how many recovery codes are left.
func (s *Service) Status(userID string) (enabled bool, recoveryLeft int, err error) {
	user, err := s.app.FindRecordById("users", userID)
	if err != nil {
		return false, 0, fmt.Errorf("error finding user: %w", err)
	}

	return Enabled(user), len(hashes(user)), nil
}

// Setup is a secret that is waiting for a code of the app, to be turned on.
type Setup struct {
	Secret string // for typing it in, in groups of four
	QR     string // the QR code as the address of a png, to put in an image
}

// StartSetup makes a new secret for the user and keeps it until it is confirmed with Enable. A secret that was waiting is replaced.
func (s *Service) StartSetup(userID string) (Setup, error) {
	user, err := s.app.FindRecordById("users", userID)
	if err != nil {
		return Setup{}, fmt.Errorf("error finding user: %w", err)
	}
	if Enabled(user) {
		return Setup{}, InvalidError{Message: "Two-factor authentication is on already."}
	}

	generated, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: user.Email(), Period: period, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return Setup{}, fmt.Errorf("error making a secret: %w", err)
	}

	sealed, err := s.key.seal(generated.Secret())
	if err != nil {
		return Setup{}, err
	}
	user.Set(secretField, sealed)
	user.Set(lastStepField, 0)
	if err := s.app.Save(user); err != nil {
		return Setup{}, fmt.Errorf("error saving the secret: %w", err)
	}

	img, err := generated.Image(qrSize, qrSize)
	if err != nil {
		return Setup{}, fmt.Errorf("error drawing the QR code: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Setup{}, fmt.Errorf("error drawing the QR code: %w", err)
	}

	return Setup{Secret: groups(generated.Secret()), QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

// CancelSetup throws away a secret that was not confirmed.
func (s *Service) CancelSetup(userID string) error {
	user, err := s.app.FindRecordById("users", userID)
	if err != nil {
		return fmt.Errorf("error finding user: %w", err)
	}
	if Enabled(user) {
		return nil
	}

	user.Set(secretField, "")

	return s.app.Save(user)
}

// Enable turns two factor authentication on, when the code of the app is right, and returns the recovery codes. They are
// not kept in a form that can be read back, so this is the only time they can be shown.
func (s *Service) Enable(userID, code string) ([]string, error) {
	user, err := s.app.FindRecordById("users", userID)
	if err != nil {
		return nil, fmt.Errorf("error finding user: %w", err)
	}
	if Enabled(user) {
		return nil, InvalidError{Message: "Two-factor authentication is on already."}
	}
	if user.GetString(secretField) == "" {
		return nil, InvalidError{Message: "Start the set up again, the secret is gone."}
	}

	if err := s.checkCode(user, code); err != nil {
		return nil, err
	}

	codes, err := s.newRecoveryCodes(user)
	if err != nil {
		return nil, err
	}
	user.Set(enabledField, true)

	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("error saving: %w", err)
	}

	return codes, nil
}

// Disable turns two factor authentication off, when the password and a code (or a recovery code) are right.
func (s *Service) Disable(userID, password, code string) error {
	user, err := s.verifyOwner(userID, password, code)
	if err != nil {
		return err
	}

	user.Set(enabledField, false)
	user.Set(secretField, "")
	user.Set(recoveryField, "")
	user.Set(lastStepField, 0)

	if err := s.app.Save(user); err != nil {
		return fmt.Errorf("error saving: %w", err)
	}

	return nil
}

// RegenerateRecoveryCodes replaces the recovery codes, when the password and a code are right. The old ones stop working.
func (s *Service) RegenerateRecoveryCodes(userID, password, code string) ([]string, error) {
	user, err := s.verifyOwner(userID, password, code)
	if err != nil {
		return nil, err
	}

	codes, err := s.newRecoveryCodes(user)
	if err != nil {
		return nil, err
	}
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("error saving: %w", err)
	}

	return codes, nil
}

// verifyOwner checks the password and a code of the user, for what only the owner of the account may do. The user is
// saved by the caller, as the code that was used is marked as used on it.
func (s *Service) verifyOwner(userID, password, code string) (*core.Record, error) {
	user, err := s.app.FindRecordById("users", userID)
	if err != nil {
		return nil, fmt.Errorf("error finding user: %w", err)
	}
	if !Enabled(user) {
		return nil, InvalidError{Message: "Two-factor authentication is not on."}
	}
	if !user.ValidatePassword(password) {
		return nil, InvalidError{Message: "The current password is not right."}
	}

	if err := s.checkAnyCode(user, code); err != nil {
		return nil, err
	}

	return user, nil
}

// VerifyLogin checks the code of the second step of signing in: a code of the app, or a recovery code, which works
// once. It returns whether a recovery code was used.
func (s *Service) VerifyLogin(userID, code string) (usedRecovery bool, err error) {
	user, err := s.app.FindRecordById("users", userID)
	if err != nil {
		return false, fmt.Errorf("error finding user: %w", err)
	}
	if !Enabled(user) {
		return false, ErrInvalid
	}

	recovery := isRecoveryCode(code)
	if err := s.checkAnyCode(user, code); err != nil {
		return false, err
	}
	if err := s.app.Save(user); err != nil {
		return false, fmt.Errorf("error saving: %w", err)
	}

	return recovery, nil
}

// checkAnyCode accepts a code of the app or a recovery code. It does not save the user.
func (s *Service) checkAnyCode(user *core.Record, code string) error {
	if isRecoveryCode(code) {
		return s.checkRecoveryCode(user, code)
	}

	return s.checkCode(user, code)
}

// checkCode checks a code of the app, and marks its step as used so it works only once. It does not save the user.
func (s *Service) checkCode(user *core.Record, code string) error {
	if !s.limiter.allow(user.Id, s.now()) {
		return ErrLocked
	}

	secret, err := s.key.open(user.GetString(secretField))
	if err != nil {
		return fmt.Errorf("error reading the secret: %w", err)
	}

	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != int(otp.DigitsSix) {
		s.limiter.fail(user.Id, s.now())
		return ErrInvalid
	}

	now := s.now().Unix() / period
	last := int64(user.GetInt(lastStepField))
	for step := now - skew; step <= now+skew; step++ {
		expected, err := hotp.GenerateCodeCustom(secret, uint64(step), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return fmt.Errorf("error making a code: %w", err)
		}

		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			if step <= last {
				// the code of a step that was used already
				s.limiter.fail(user.Id, s.now())
				return ErrInvalid
			}

			user.Set(lastStepField, step)
			s.limiter.reset(user.Id)

			return nil
		}
	}

	s.limiter.fail(user.Id, s.now())

	return ErrInvalid
}

// checkRecoveryCode checks a recovery code and takes it away from the user, so it works once. It does not save the user.
func (s *Service) checkRecoveryCode(user *core.Record, code string) error {
	if !s.limiter.allow(user.Id, s.now()) {
		return ErrLocked
	}

	given := hashRecoveryCode(code)
	kept := make([]string, 0, recoveryCount)
	found := false
	for _, h := range hashes(user) {
		// all of them are compared, whichever one it is, so the time it takes tells nothing
		if subtle.ConstantTimeCompare([]byte(h), []byte(given)) == 1 {
			found = true
			continue
		}
		kept = append(kept, h)
	}

	if !found {
		s.limiter.fail(user.Id, s.now())
		return ErrInvalid
	}

	user.Set(recoveryField, strings.Join(kept, ","))
	s.limiter.reset(user.Id)

	return nil
}

// newRecoveryCodes makes the codes and keeps their hashes on the user. It does not save the user.
func (s *Service) newRecoveryCodes(user *core.Record) ([]string, error) {
	codes := make([]string, 0, recoveryCount)
	stored := make([]string, 0, recoveryCount)

	for len(codes) < recoveryCount {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("error making a recovery code: %w", err)
		}

		var b strings.Builder
		for i, v := range raw {
			if i == 5 {
				b.WriteByte('-')
			}
			b.WriteByte(recoveryChars[int(v)%len(recoveryChars)])
		}

		codes = append(codes, b.String())
		stored = append(stored, hashRecoveryCode(b.String()))
	}

	user.Set(recoveryField, strings.Join(stored, ","))

	return codes, nil
}

// isRecoveryCode tells a recovery code from a code of the app: the codes of the app are only digits.
func isRecoveryCode(code string) bool {
	for _, r := range strings.TrimSpace(code) {
		if (r < '0' || r > '9') && r != ' ' {
			return true
		}
	}

	return false
}

func hashRecoveryCode(code string) string {
	normalized := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte(normalized))

	return hex.EncodeToString(sum[:])
}

func hashes(user *core.Record) []string {
	stored := user.GetString(recoveryField)
	if stored == "" {
		return nil
	}

	return strings.Split(stored, ",")
}

// groups writes the secret in groups of four, to type it in: ABCD EFGH.
func groups(secret string) string {
	var b strings.Builder
	for i, r := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}

	return b.String()
}

// Challenge is the proof that the password was right, which the second step of signing in comes with. It is a token
// that is signed with the key, so it can not be made or changed by anyone else, and it is only good for a short while.
type Challenge struct {
	UserID   string
	Remember bool
}

// NewChallenge makes the token for the second step.
func (s *Service) NewChallenge(userID string, remember bool) string {
	payload := userID + "|" + strconv.FormatInt(s.now().Add(ChallengeTTL).Unix(), 10) + "|" + strconv.FormatBool(remember)

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload))
}

// ParseChallenge reads the token of the second step. It fails when it was changed, or is too old.
func (s *Service) ParseChallenge(token string) (Challenge, bool) {
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok {
		return Challenge{}, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Challenge{}, false
	}
	given, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(given, s.sign(string(payload))) {
		return Challenge{}, false
	}

	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 {
		return Challenge{}, false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || s.now().Unix() > expires {
		return Challenge{}, false
	}

	return Challenge{UserID: parts[0], Remember: parts[2] == "true"}, true
}

func (s *Service) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("lavender-mfa-challenge|"))
	mac.Write([]byte(payload))

	return mac.Sum(nil)
}

// limiter counts the wrong codes of a user, in memory: it is enough to stop someone from trying all the codes, and the
// count starting again when the server does is no help to them.
type limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func (l *limiter) allow(userID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.recent(userID, now)) < MaxFailures
}

func (l *limiter) fail(userID string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failures[userID] = append(l.recent(userID, now), now)
}

func (l *limiter) reset(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.failures, userID)
}

// recent are the failures that are still counted. The caller has the lock.
func (l *limiter) recent(userID string, now time.Time) []time.Time {
	recent := l.failures[userID][:0:0]
	for _, t := range l.failures[userID] {
		if now.Sub(t) < FailureWindow {
			recent = append(recent, t)
		}
	}

	if len(recent) == 0 {
		delete(l.failures, userID)
	} else {
		l.failures[userID] = recent
	}

	return recent
}
