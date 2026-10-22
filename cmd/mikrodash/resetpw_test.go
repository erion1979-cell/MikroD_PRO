package main

import (
	"bytes"
	"strings"
	"testing"

	"mikrodash/internal/store"
)

func resetFixture(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []store.NewUser{{Username: "Admin", Password: "old-one", Role: "admin"},
		{Username: "viewer", Password: "theirs", Role: "viewer"}} {
		if _, err := st.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	return dir, st
}

func passwordWorks(t *testing.T, st *store.Store, name, pw string) bool {
	t.Helper()
	users, err := st.Users()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == name {
			return store.VerifyPassword(u, pw)
		}
	}
	t.Fatalf("no user %s", name)
	return false
}

// A RESET CHANGES ONE ACCOUNT AND NOTHING ELSE: the new password signs in, the
// old one does not, and the other user's password still works.
func TestAResetChangesOnlyThatAccount(t *testing.T) {
	dir, st := resetFixture(t)
	var out bytes.Buffer
	// Typed twice at a terminal; the name matched ignoring case.
	if code := runResetPassword(dir, "admin", strings.NewReader("new-secret\nnew-secret\n"), &out, true); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !passwordWorks(t, st, "Admin", "new-secret") || passwordWorks(t, st, "Admin", "old-one") {
		t.Error("the new password does not work, or the old one still does")
	}
	if !passwordWorks(t, st, "viewer", "theirs") {
		t.Error("another user's password changed")
	}
	if strings.Contains(out.String(), "new-secret") {
		t.Error("the password was echoed back")
	}
}

func TestAResetIsRefusedWithoutHarm(t *testing.T) {
	cases := map[string]struct {
		user, input string
		terminal    bool
		want        string
	}{
		"two different entries": {"Admin", "one-pass\nother-pass\n", true, "differ"},
		"too short":              {"Admin", "abc\n", false, "at least"},
		"no such user":           {"root", "long-enough\n", false, "the users are: Admin, viewer"},
		"nothing typed":          {"Admin", "", false, "no password"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, st := resetFixture(t)
			var out bytes.Buffer
			if code := runResetPassword(dir, c.user, strings.NewReader(c.input), &out, c.terminal); code == 0 {
				t.Fatal("accepted")
			}
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("said %q, want it to mention %q", out.String(), c.want)
			}
			if !passwordWorks(t, st, "Admin", "old-one") {
				t.Error("a refused reset changed the password")
			}
		})
	}
}
