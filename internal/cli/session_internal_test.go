package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

func TestSessionStatus(t *testing.T) {
	const (
		idleTTL = 30 * time.Minute
		maxTTL  = 10 * time.Hour
	)

	opened := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

	newSession := func() *session.Session {
		created, err := session.New(uuid.New(), uuid.New(), opened, maxTTL, "", "")
		if err != nil {
			t.Fatalf("opening a session: %v", err)
		}

		return created
	}

	revoked := newSession()
	if err := revoked.End(opened.Add(time.Minute), session.EndRevoked); err != nil {
		t.Fatalf("ending a session: %v", err)
	}

	// Seen just before the absolute deadline, so only that deadline can expire it.
	active := newSession()
	active.Touch(opened.Add(maxTTL - time.Minute))

	cases := map[string]struct {
		session *session.Session
		now     time.Time
		want    string
	}{
		"live":                       {session: newSession(), now: opened.Add(time.Minute), want: "live"},
		"idle for too long":          {session: newSession(), now: opened.Add(idleTTL), want: "expired"},
		"past the absolute deadline": {session: active, now: opened.Add(maxTTL), want: "expired"},
		"ended":                      {session: revoked, now: opened.Add(2 * time.Minute), want: "ended:revoked"},
		// The reason an operator ended it outlives the deadline.
		"ended and past the deadline": {session: revoked, now: opened.Add(2 * maxTTL), want: "ended:revoked"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sessionStatus(tc.session, tc.now, idleTTL); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestShorten(t *testing.T) {
	cases := map[string]struct {
		value string
		want  string
	}{
		"shorter than the limit": {value: "curl/8.7.1", want: "curl/8.7.1"},
		"exactly the limit":      {value: strings.Repeat("a", 12), want: strings.Repeat("a", 12)},
		"longer than the limit":  {value: strings.Repeat("a", 13), want: strings.Repeat("a", 9) + "..."},
		// Cut by rune: a byte cut would leave half a character on the terminal.
		"multibyte": {value: strings.Repeat("é", 13), want: strings.Repeat("é", 9) + "..."},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := shorten(tc.value, 12); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}
