package webapp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTicketTTL     = 2 * time.Minute
	defaultSessionTTL    = 15 * time.Minute
	initDataMaxAge       = 5 * time.Minute
	initDataFutureSkew   = time.Minute
	maxTrackedPrincipals = 4096
)

var (
	ErrInvalidInitData = errors.New("Telegram init data is invalid")
	ErrInvalidTicket   = errors.New("Mini App launch ticket is invalid or expired")
	ErrSessionExpired  = errors.New("Mini App session is invalid or expired")
	ErrNotAllowed      = errors.New("Telegram user is not allowed")
	// ErrPrivateOnly — the Mini App is a private-chat companion and issues no ticket for a group chat.
	ErrPrivateOnly = errors.New("the Mini App is available only in private chats")
)

type scope struct {
	ChatID int64
	UserID int64
}

type launchTicket struct {
	scope     scope
	expiresAt time.Time
}

type bearerSession struct {
	scope     scope
	expiresAt time.Time
}

type authenticator struct {
	mu         sync.Mutex
	botToken   string
	authorize  func(int64) bool
	now        func() time.Time
	ticketTTL  time.Duration
	sessionTTL time.Duration
	tickets    map[string]launchTicket
	sessions   map[[sha256.Size]byte]bearerSession
}

func newAuthenticator(botToken string, authorize func(int64) bool) *authenticator {
	return &authenticator{
		botToken: botToken, authorize: authorize, now: time.Now,
		ticketTTL: defaultTicketTTL, sessionTTL: defaultSessionTTL,
		tickets: map[string]launchTicket{}, sessions: map[[sha256.Size]byte]bearerSession{},
	}
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("secure random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (a *authenticator) NewLaunchTicket(chatID int64, userID int64) (string, error) {
	if chatID == 0 || userID <= 0 || !a.authorize(userID) {
		return "", ErrNotAllowed
	}
	// The session is scoped to the chat, so a group ticket would hand one member the history, documents and
	// reminders of another. A private chat is the only place where the two scopes coincide.
	if chatID != userID {
		return "", ErrPrivateOnly
	}
	ticket, err := randomToken(16)
	if err != nil {
		return "", err
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(now)
	if len(a.tickets) >= maxTrackedPrincipals {
		return "", errors.New("Mini App launch ticket capacity is exhausted")
	}
	a.tickets[ticket] = launchTicket{scope: scope{ChatID: chatID, UserID: userID}, expiresAt: now.Add(a.ticketTTL)}
	return ticket, nil
}

func verifyInitData(raw string, botToken string, now time.Time) (int64, error) {
	if raw == "" || len(raw) > maxBodyBytes || strings.TrimSpace(botToken) == "" {
		return 0, ErrInvalidInitData
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, ErrInvalidInitData
	}
	for key, entries := range values {
		if len(entries) != 1 || (key == "" || entries[0] == "" && key != "query_id") {
			return 0, ErrInvalidInitData
		}
	}
	receivedHash, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(receivedHash) != sha256.Size {
		return 0, ErrInvalidInitData
	}
	authSeconds, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authSeconds <= 0 {
		return 0, ErrInvalidInitData
	}
	authTime := time.Unix(authSeconds, 0)
	age := now.Sub(authTime)
	if age > initDataMaxAge || age < -initDataFutureSkew {
		return 0, ErrInvalidInitData
	}

	keys := make([]string, 0, len(values)-1)
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = mac.Write([]byte(strings.Join(lines, "\n")))
	if subtle.ConstantTimeCompare(mac.Sum(nil), receivedHash) != 1 {
		return 0, ErrInvalidInitData
	}

	var signedUser struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &signedUser); err != nil || signedUser.ID <= 0 {
		return 0, ErrInvalidInitData
	}
	return signedUser.ID, nil
}

func (a *authenticator) exchange(raw string, ticket string) (string, time.Time, error) {
	now := a.now()
	userID, err := verifyInitData(raw, a.botToken, now)
	if err != nil {
		return "", time.Time{}, err
	}
	if !a.authorize(userID) {
		return "", time.Time{}, ErrNotAllowed
	}

	launch := launchTicket{scope: scope{ChatID: userID, UserID: userID}}
	a.mu.Lock()
	if ticket == "" {
		a.pruneLocked(now)
	} else {
		ticketLaunch, found := a.tickets[ticket]
		if found {
			delete(a.tickets, ticket)
		}
		a.pruneLocked(now)
		if !found || !now.Before(ticketLaunch.expiresAt) || ticketLaunch.scope.UserID != userID {
			a.mu.Unlock()
			return "", time.Time{}, ErrInvalidTicket
		}
		launch = ticketLaunch
	}
	bearer, err := randomToken(32)
	if err != nil {
		a.mu.Unlock()
		return "", time.Time{}, err
	}
	if len(a.sessions) >= maxTrackedPrincipals {
		a.mu.Unlock()
		return "", time.Time{}, errors.New("Mini App session capacity is exhausted")
	}
	expiresAt := now.Add(a.sessionTTL)
	a.sessions[sha256.Sum256([]byte(bearer))] = bearerSession{scope: launch.scope, expiresAt: expiresAt}
	a.mu.Unlock()
	return bearer, expiresAt, nil
}

func (a *authenticator) authorizeBearer(bearer string) (scope, error) {
	if bearer == "" || len(bearer) > 128 {
		return scope{}, ErrSessionExpired
	}
	now := a.now()
	key := sha256.Sum256([]byte(bearer))
	a.mu.Lock()
	defer a.mu.Unlock()
	session, found := a.sessions[key]
	if !found || !now.Before(session.expiresAt) || !a.authorize(session.scope.UserID) {
		delete(a.sessions, key)
		return scope{}, ErrSessionExpired
	}
	return session.scope, nil
}

func (a *authenticator) revoke(bearer string) {
	a.mu.Lock()
	delete(a.sessions, sha256.Sum256([]byte(bearer)))
	a.mu.Unlock()
}

func (a *authenticator) pruneLocked(now time.Time) {
	for ticket, launch := range a.tickets {
		if !now.Before(launch.expiresAt) {
			delete(a.tickets, ticket)
		}
	}
	for key, session := range a.sessions {
		if !now.Before(session.expiresAt) {
			delete(a.sessions, key)
		}
	}
}
