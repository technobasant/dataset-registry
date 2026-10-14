package auth

import (
	"strings"
	"testing"
	"time"
)

func TestIssueVerifyRoundTrip(t *testing.T) {
	i, err := NewIssuer([]byte(strings.Repeat("k", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := Principal{Subject: "amara", Role: Owner, Team: "checkout"}
	tok, err := i.Issue(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := i.Verify(tok)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	i, _ := NewIssuer(key, time.Hour)
	other, _ := NewIssuer([]byte(strings.Repeat("o", 32)), time.Hour)
	tok, _ := other.Issue(Principal{Subject: "x", Role: Admin, Team: "t"})
	if _, err := i.Verify(tok); err == nil {
		t.Error("token signed with another key was accepted")
	}

	expired, _ := NewIssuer(key, time.Hour)
	expired.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	tok, _ = expired.Issue(Principal{Subject: "x", Role: Admin, Team: "t"})
	if _, err := i.Verify(tok); err == nil {
		t.Error("expired token was accepted")
	}

	if _, err := i.Issue(Principal{Subject: "x", Role: "root", Team: "t"}); err == nil {
		t.Error("unknown role was issued")
	}
	if _, err := NewIssuer([]byte("short"), time.Hour); err == nil {
		t.Error("short key accepted")
	}
}
