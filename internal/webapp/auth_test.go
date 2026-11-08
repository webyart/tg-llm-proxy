package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const officialFixtureToken = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

const officialFixture = "query_id=AAHdF6IQAAAAAN0XohDhrOrc&user=%7B%22id%22%3A279058397%2C%22first_name%22%3A%22Vladislav%22%2C%22last_name%22%3A%22Kibenko%22%2C%22username%22%3A%22vdkfrost%22%2C%22language_code%22%3A%22ru%22%2C%22is_premium%22%3Atrue%7D&auth_date=1662771648&hash=7a789370203900df724df4b8b4945aaefb7ce5898c70be807cf76c2426ffec49"

func signedInitData(t *testing.T, token string, userID int64, authDate time.Time) string {
	t.Helper()
	user, err := json.Marshal(map[string]any{"id": userID, "first_name": "Test"})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"auth_date": {strconv.FormatInt(authDate.Unix(), 10)}, "user": {string(user)}, "query_id": {"AAEtest"}}
	keys := []string{"auth_date", "query_id", "user"}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = mac.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}

func TestOfficialInitDataHMACFixture(t *testing.T) {
	userID, err := verifyInitData(officialFixture, officialFixtureToken, time.Unix(1662771648, 0))
	if err != nil || userID != 279058397 {
		t.Fatalf("official fixture user=%d err=%v", userID, err)
	}
	if _, err := verifyInitData(officialFixture, "123456:wrong", time.Unix(1662771648, 0)); !errors.Is(err, ErrInvalidInitData) {
		t.Fatalf("wrong token err=%v", err)
	}
	tampered := strings.Replace(officialFixture, "Vladislav", "Mallory", 1)
	if _, err := verifyInitData(tampered, officialFixtureToken, time.Unix(1662771648, 0)); !errors.Is(err, ErrInvalidInitData) {
		t.Fatalf("tampered signed user err=%v", err)
	}
}

func TestInitDataAuthDateFreshnessAndShape(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, test := range []struct {
		name string
		date time.Time
	}{
		{"expired", now.Add(-initDataMaxAge - time.Second)},
		{"future", now.Add(initDataFutureSkew + time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := signedInitData(t, "123:test", 1717, test.date)
			if _, err := verifyInitData(raw, "123:test", now); !errors.Is(err, ErrInvalidInitData) {
				t.Fatalf("auth_date=%v err=%v", test.date, err)
			}
		})
	}
	raw := signedInitData(t, "123:test", 1717, now)
	for _, invalid := range []string{
		raw + "&auth_date=1700000000", strings.Replace(raw, "hash=", "hash=00", 1),
		strings.Replace(raw, "user=", "user=not-json&other=", 1),
	} {
		if _, err := verifyInitData(invalid, "123:test", now); !errors.Is(err, ErrInvalidInitData) {
			t.Errorf("invalid initData accepted: %v", err)
		}
	}
}

func TestDirectLaunchUsesOwnPrivateScope(t *testing.T) {
	auth := newAuthenticator("123:test", func(userID int64) bool { return userID == 1717 })
	now := time.Unix(1700000000, 0)
	auth.now = func() time.Time { return now }
	raw := signedInitData(t, "123:test", 1717, now)
	bearer, _, err := auth.exchange(raw, "")
	if err != nil || bearer == "" {
		t.Fatalf("direct exchange bearer=%q err=%v", bearer, err)
	}
	current, err := auth.authorizeBearer(bearer)
	if err != nil || current.ChatID != 1717 || current.UserID != 1717 {
		t.Fatalf("direct session scope=%+v err=%v", current, err)
	}
}

func TestDirectLaunchRejectsDisallowedUser(t *testing.T) {
	auth := newAuthenticator("123:test", func(int64) bool { return false })
	now := time.Unix(1700000000, 0)
	auth.now = func() time.Time { return now }
	raw := signedInitData(t, "123:test", 1717, now)
	if _, _, err := auth.exchange(raw, ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("disallowed direct launch err=%v", err)
	}
}

func TestDirectLaunchRejectsInvalidInitData(t *testing.T) {
	auth := newAuthenticator("123:test", func(int64) bool { return true })
	now := time.Unix(1700000000, 0)
	auth.now = func() time.Time { return now }
	raw := signedInitData(t, "123:wrong", 1717, now)
	if _, _, err := auth.exchange(raw, ""); !errors.Is(err, ErrInvalidInitData) {
		t.Fatalf("invalid direct launch initData err=%v", err)
	}
}

func TestTicketScopeSingleUseAndSessionExpiry(t *testing.T) {
	allowed := true
	auth := newAuthenticator("123:test", func(int64) bool { return allowed })
	now := time.Unix(1700000000, 0)
	auth.now = func() time.Time { return now }
	ticket, err := auth.NewLaunchTicket(1717, 1717)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("ticket is not random 128-bit data: bytes=%d err=%v", len(decoded), err)
	}
	raw := signedInitData(t, "123:test", 1717, now)
	bearer, _, err := auth.exchange(raw, ticket)
	if err != nil || bearer == "" {
		t.Fatalf("exchange bearer=%q err=%v", bearer, err)
	}
	current, err := auth.authorizeBearer(bearer)
	if err != nil || current.ChatID != 1717 || current.UserID != 1717 {
		t.Fatalf("session scope=%+v err=%v", current, err)
	}
	if _, _, err := auth.exchange(raw, ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("ticket replay err=%v", err)
	}

	now = now.Add(defaultSessionTTL + time.Second)
	if _, err := auth.authorizeBearer(bearer); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired session err=%v", err)
	}
}

func TestGroupTicketIsRefused(t *testing.T) {
	auth := newAuthenticator("123:test", func(int64) bool { return true })
	if _, err := auth.NewLaunchTicket(-100500, 1717); !errors.Is(err, ErrPrivateOnly) {
		t.Fatalf("group ticket err=%v, want ErrPrivateOnly", err)
	}
}

func TestTicketRejectsWrongUserAndExpiry(t *testing.T) {
	auth := newAuthenticator("123:test", func(int64) bool { return true })
	now := time.Unix(1700000000, 0)
	auth.now = func() time.Time { return now }
	ticket, err := auth.NewLaunchTicket(1717, 1717)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.exchange(signedInitData(t, "123:test", 9999, now), ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("wrong user err=%v", err)
	}
	ticket, err = auth.NewLaunchTicket(1717, 1717)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(defaultTicketTTL + time.Second)
	if _, _, err := auth.exchange(signedInitData(t, "123:test", 1717, now), ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("expired ticket err=%v", err)
	}
}

func TestEverySessionRequestRechecksCurrentAllowlist(t *testing.T) {
	allowed := true
	auth := newAuthenticator("123:test", func(int64) bool { return allowed })
	now := time.Unix(1700000000, 0)
	auth.now = func() time.Time { return now }
	ticket, _ := auth.NewLaunchTicket(1717, 1717)
	bearer, _, err := auth.exchange(signedInitData(t, "123:test", 1717, now), ticket)
	if err != nil {
		t.Fatal(err)
	}
	allowed = false
	if _, err := auth.authorizeBearer(bearer); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("removed allowlist user err=%v", err)
	}
	allowed = true
	if _, err := auth.authorizeBearer(bearer); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("revoked session was resurrected: %v", err)
	}
}
