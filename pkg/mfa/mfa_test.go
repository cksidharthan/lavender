package mfa

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/cksidharthan/lavender/pkg/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	t      *testing.T
	app    core.App
	svc    *Service
	userID string
	clock  time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)
	require.NoError(t, app.RunAppMigrations())
	require.NoError(t, app.ReloadCachedCollections())

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	u := core.NewRecord(users)
	u.SetEmail("alice@example.com")
	u.SetPassword("password123")
	require.NoError(t, app.Save(u))

	key, err := LoadKey("", t.TempDir())
	require.NoError(t, err)

	f := &fixture{t: t, app: app, svc: New(app, key), userID: u.Id, clock: time.Date(2026, 10, 23, 12, 0, 10, 0, time.UTC)}
	f.svc.now = func() time.Time { return f.clock }

	return f
}

// codeAt is the code of the app for the secret at the time.
func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()

	code, err := hotp.GenerateCodeCustom(strings.ReplaceAll(secret, " ", ""), uint64(at.Unix()/period), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	require.NoError(t, err)

	return code
}

// enabled sets two factor authentication on, and returns the secret and the recovery codes.
func (f *fixture) enabled() (string, []string) {
	f.t.Helper()

	setup, err := f.svc.StartSetup(f.userID)
	require.NoError(f.t, err)
	codes, err := f.svc.Enable(f.userID, codeAt(f.t, setup.Secret, f.clock))
	require.NoError(f.t, err)

	return setup.Secret, codes
}

func TestSetupAndEnable(t *testing.T) {
	f := newFixture(t)

	enabled, left, err := f.svc.Status(f.userID)
	require.NoError(t, err)
	require.False(t, enabled)
	require.Zero(t, left)

	setup, err := f.svc.StartSetup(f.userID)
	require.NoError(t, err)
	require.Regexp(t, `^([A-Z2-7]{4} )+[A-Z2-7]{4}$`, setup.Secret, "in groups of four")
	require.True(t, strings.HasPrefix(setup.QR, "data:image/png;base64,"))
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(setup.QR, "data:image/png;base64,"))
	require.NoError(t, err)
	require.Equal(t, []byte{0x89, 'P', 'N', 'G'}, png[:4])

	// the secret is sealed, not stored as it is
	user, err := f.app.FindRecordById("users", f.userID)
	require.NoError(t, err)
	require.NotContains(t, user.GetString(secretField), strings.ReplaceAll(setup.Secret, " ", ""))
	require.False(t, Enabled(user), "not on until the code is right")

	// a wrong code, and a code of another secret
	_, err = f.svc.Enable(f.userID, "000000")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = f.svc.Enable(f.userID, "12345")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = f.svc.Enable(f.userID, "abcdef")
	require.Error(t, err)

	codes, err := f.svc.Enable(f.userID, codeAt(t, setup.Secret, f.clock))
	require.NoError(t, err)
	require.Len(t, codes, recoveryCount)
	for _, c := range codes {
		require.Regexp(t, `^[a-z2-9]{5}-[a-z2-9]{5}$`, c)
	}
	require.Len(t, map[string]bool{codes[0]: true, codes[1]: true, codes[2]: true}, 3, "they are different")

	enabled, left, err = f.svc.Status(f.userID)
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, recoveryCount, left)

	// the recovery codes are not kept as they are
	user, err = f.app.FindRecordById("users", f.userID)
	require.NoError(t, err)
	for _, c := range codes {
		require.NotContains(t, user.GetString(recoveryField), strings.ReplaceAll(c, "-", ""))
	}

	var invalid InvalidError
	_, err = f.svc.StartSetup(f.userID)
	require.ErrorAs(t, err, &invalid, "on already")
	_, err = f.svc.Enable(f.userID, codeAt(t, setup.Secret, f.clock.Add(time.Minute)))
	require.ErrorAs(t, err, &invalid, "on already")
}

func TestStartingSetupAgainReplacesTheSecret(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.StartSetup(f.userID)
	require.NoError(t, err)
	second, err := f.svc.StartSetup(f.userID)
	require.NoError(t, err)
	require.NotEqual(t, first.Secret, second.Secret)

	_, err = f.svc.Enable(f.userID, codeAt(t, first.Secret, f.clock))
	require.ErrorIs(t, err, ErrInvalid, "the code of the first secret is not good any more")
	_, err = f.svc.Enable(f.userID, codeAt(t, second.Secret, f.clock))
	require.NoError(t, err)

	// cancelling throws it away
	g := newFixture(t)
	setup, err := g.svc.StartSetup(g.userID)
	require.NoError(t, err)
	require.NoError(t, g.svc.CancelSetup(g.userID))
	var invalid InvalidError
	_, err = g.svc.Enable(g.userID, codeAt(t, setup.Secret, g.clock))
	require.ErrorAs(t, err, &invalid)
}

func TestLoginCodes(t *testing.T) {
	f := newFixture(t)
	secret, _ := f.enabled()

	// the step that enabled it was used, a code is good once
	f.clock = f.clock.Add(31 * time.Second)
	code := codeAt(t, secret, f.clock)

	used, err := f.svc.VerifyLogin(f.userID, code)
	require.NoError(t, err)
	require.False(t, used)

	_, err = f.svc.VerifyLogin(f.userID, code)
	require.ErrorIs(t, err, ErrInvalid, "the same code twice")

	// the codes of the step before and after are taken, a minute away is not
	f.clock = f.clock.Add(31 * time.Second)
	next := codeAt(t, secret, f.clock.Add(30*time.Second))
	_, err = f.svc.VerifyLogin(f.userID, next)
	require.NoError(t, err, "a clock that is a little ahead")

	f.clock = f.clock.Add(31 * time.Second)
	_, err = f.svc.VerifyLogin(f.userID, codeAt(t, secret, f.clock.Add(-70*time.Second)))
	require.ErrorIs(t, err, ErrInvalid, "an old code")

	// with spaces, as an app shows it
	f.clock = f.clock.Add(31 * time.Second)
	c := codeAt(t, secret, f.clock)
	_, err = f.svc.VerifyLogin(f.userID, c[:3]+" "+c[3:])
	require.NoError(t, err)

	_, err = f.svc.VerifyLogin("nobody", "123456")
	require.Error(t, err)
}

func TestRecoveryCodesWorkOnce(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enabled()

	used, err := f.svc.VerifyLogin(f.userID, strings.ToUpper(codes[0])) // any case
	require.NoError(t, err)
	require.True(t, used)

	_, err = f.svc.VerifyLogin(f.userID, codes[0])
	require.ErrorIs(t, err, ErrInvalid, "once")

	used, err = f.svc.VerifyLogin(f.userID, strings.ReplaceAll(codes[1], "-", " ")) // without the dash
	require.NoError(t, err)
	require.True(t, used)

	_, left, err := f.svc.Status(f.userID)
	require.NoError(t, err)
	require.Equal(t, recoveryCount-2, left)

	_, err = f.svc.VerifyLogin(f.userID, "aaaaa-aaaaa")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestTooManyWrongCodesLockForAWhile(t *testing.T) {
	f := newFixture(t)
	secret, codes := f.enabled()
	f.clock = f.clock.Add(31 * time.Second)

	for range MaxFailures {
		_, err := f.svc.VerifyLogin(f.userID, "000000")
		require.ErrorIs(t, err, ErrInvalid)
	}

	// even the right codes do not work now
	_, err := f.svc.VerifyLogin(f.userID, codeAt(t, secret, f.clock))
	require.ErrorIs(t, err, ErrLocked)
	_, err = f.svc.VerifyLogin(f.userID, codes[0])
	require.ErrorIs(t, err, ErrLocked)

	f.clock = f.clock.Add(FailureWindow + time.Second)
	_, err = f.svc.VerifyLogin(f.userID, codeAt(t, secret, f.clock))
	require.NoError(t, err, "later it works again")

	// a right code starts the count again
	for range MaxFailures - 1 {
		_, err := f.svc.VerifyLogin(f.userID, "000000")
		require.ErrorIs(t, err, ErrInvalid)
	}
	f.clock = f.clock.Add(31 * time.Second)
	_, err = f.svc.VerifyLogin(f.userID, codeAt(t, secret, f.clock))
	require.NoError(t, err)
	for range MaxFailures - 1 {
		_, err := f.svc.VerifyLogin(f.userID, "000000")
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestDisableAndRegenerate(t *testing.T) {
	f := newFixture(t)
	secret, codes := f.enabled()
	var invalid InvalidError

	f.clock = f.clock.Add(31 * time.Second)
	_, err := f.svc.RegenerateRecoveryCodes(f.userID, "wrong", codeAt(t, secret, f.clock))
	require.ErrorAs(t, err, &invalid)
	require.Contains(t, invalid.Message, "password")

	newCodes, err := f.svc.RegenerateRecoveryCodes(f.userID, "password123", codeAt(t, secret, f.clock))
	require.NoError(t, err)
	require.Len(t, newCodes, recoveryCount)
	_, err = f.svc.VerifyLogin(f.userID, codes[0])
	require.ErrorIs(t, err, ErrInvalid, "the old codes stop working")
	_, err = f.svc.VerifyLogin(f.userID, newCodes[0])
	require.NoError(t, err)

	// a recovery code works for it too
	require.ErrorAs(t, f.svc.Disable(f.userID, "wrong", newCodes[1]), &invalid)
	require.ErrorIs(t, f.svc.Disable(f.userID, "password123", "000000"), ErrInvalid)
	require.NoError(t, f.svc.Disable(f.userID, "password123", newCodes[1]))

	enabled, left, err := f.svc.Status(f.userID)
	require.NoError(t, err)
	require.False(t, enabled)
	require.Zero(t, left)
	require.ErrorAs(t, f.svc.Disable(f.userID, "password123", "000000"), &invalid, "it is off")

	user, err := f.app.FindRecordById("users", f.userID)
	require.NoError(t, err)
	require.Empty(t, user.GetString(secretField), "the secret is gone")

	// it can be set up again, with a new secret
	again, err := f.svc.StartSetup(f.userID)
	require.NoError(t, err)
	require.NotEqual(t, strings.ReplaceAll(secret, " ", ""), strings.ReplaceAll(again.Secret, " ", ""))
}

func TestChallenge(t *testing.T) {
	f := newFixture(t)

	token := f.svc.NewChallenge("user1", true)
	got, ok := f.svc.ParseChallenge(token)
	require.True(t, ok)
	require.Equal(t, Challenge{UserID: "user1", Remember: true}, got)

	got, ok = f.svc.ParseChallenge(f.svc.NewChallenge("user1", false))
	require.True(t, ok)
	require.False(t, got.Remember)

	// changed, made with another key, cut, or old
	payload, sig, _ := strings.Cut(token, ".")
	forged := base64.RawURLEncoding.EncodeToString([]byte("someone-else|9999999999|true")) + "." + sig
	for name, bad := range map[string]string{"empty": "", "no dot": "abc", "forged payload": forged, "no signature": payload + ".", "garbage": "!!.!!"} {
		_, ok := f.svc.ParseChallenge(bad)
		require.False(t, ok, name)
	}

	otherKey, err := LoadKey("", t.TempDir())
	require.NoError(t, err)
	_, ok = New(f.app, otherKey).ParseChallenge(token)
	require.False(t, ok, "another key")

	f.clock = f.clock.Add(ChallengeTTL + time.Second)
	_, ok = f.svc.ParseChallenge(token)
	require.False(t, ok, "too old")
}

func TestKey(t *testing.T) {
	dir := t.TempDir()

	// made the first time, and the same after
	first, err := LoadKey("", dir)
	require.NoError(t, err)
	require.Len(t, first, keyBytes)
	info, err := os.Stat(filepath.Join(dir, KeyFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the owner reads it")
	second, err := LoadKey("", dir)
	require.NoError(t, err)
	require.Equal(t, first, second)

	// from the configuration
	given := base64.StdEncoding.EncodeToString(make([]byte, keyBytes))
	fromConfig, err := LoadKey(given, dir)
	require.NoError(t, err)
	require.Equal(t, Key(make([]byte, keyBytes)), fromConfig)

	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("too short"))} {
		_, err := LoadKey(bad, dir)
		require.Error(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, KeyFileName), []byte("garbage"), 0o600))
	_, err = LoadKey("", dir)
	require.Error(t, err, "a key file that is not valid is not replaced by a new key")

	// sealing
	sealed, err := first.seal("a secret")
	require.NoError(t, err)
	require.NotContains(t, sealed, "secret")
	opened, err := first.open(sealed)
	require.NoError(t, err)
	require.Equal(t, "a secret", opened)
	again, err := first.seal("a secret")
	require.NoError(t, err)
	require.NotEqual(t, sealed, again, "a new nonce each time")
	_, err = fromConfig.open(sealed)
	require.Error(t, err, "another key")
	_, err = first.open(sealed[:len(sealed)-4] + "AAAA")
	require.Error(t, err, "changed")
	_, err = first.open("x")
	require.Error(t, err)
}
