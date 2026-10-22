package main

// `mikrodash -reset-password <username>`: set a new password for one account
// from the machine MikroDash runs on, for an operator locked out of the web
// page. Nothing else in /data is touched.
//
//	docker exec -it <container> mikrodash -data /data -reset-password admin
//
// ── IT RUNS BESIDE A RUNNING SERVER ─────────────────────────────────────────
//
// The server reads users.json at every sign-in and `store.SetPassword` writes
// it atomically, replacing only the one record, so the new password works at
// once and the container need not be stopped. Signed-in sessions are left as
// they are.
//
// ── THE PASSWORD IS NEVER AN ARGUMENT ───────────────────────────────────────
//
// An argument is visible to every user of the machine in the process list and
// lands in the shell history. It is typed, twice, with echo off where a
// terminal allows it; piped in, one line is read.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"mikrodash/internal/store"
)

// runResetPassword is the command: 0 on success, 1 on a refusal or failure,
// with the reason written to out.
func runResetPassword(dataDir, username string, in io.Reader, out io.Writer, terminal bool) int {
	st, err := store.Open(dataDir)
	if err != nil {
		fmt.Fprintf(out, "cannot open %s: %v\n", dataDir, err)
		return 1
	}
	pw, err := readNewPassword(in, out, terminal)
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	if err := resetPassword(st, username, pw); err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	fmt.Fprintf(out, "The password of %q is changed. Sign in with it now; nothing needs restarting.\n", username)
	return 0
}

// resetPassword sets the password of the account named username: matched
// exactly, or ignoring case when that names exactly one account.
func resetPassword(st *store.Store, username, pw string) error {
	if len(pw) < store.MinPasswordLen {
		return fmt.Errorf("the password must be at least %d characters; nothing was changed", store.MinPasswordLen)
	}
	users, err := st.Users()
	if err != nil {
		return fmt.Errorf("cannot read the users: %w", err)
	}
	var id string
	var folded []store.User
	for _, u := range users {
		if u.Username == username {
			id = u.ID
		}
		if strings.EqualFold(u.Username, username) {
			folded = append(folded, u)
		}
	}
	if id == "" && len(folded) == 1 {
		id = folded[0].ID
	}
	if id == "" {
		names := make([]string, 0, len(users))
		for _, u := range users {
			names = append(names, u.Username)
		}
		if len(names) == 0 {
			return errors.New("there are no users yet: open MikroDash in a browser to create the first one")
		}
		return fmt.Errorf("no user %q; the users are: %s", username, strings.Join(names, ", "))
	}
	return st.SetPassword(id, pw)
}

// readNewPassword asks twice on a terminal, with echo off where stty allows,
// and reads one line otherwise.
func readNewPassword(in io.Reader, out io.Writer, terminal bool) (string, error) {
	r := bufio.NewReader(in)
	line := func(prompt string) (string, error) {
		if terminal {
			fmt.Fprint(out, prompt)
		}
		s, err := r.ReadString('\n')
		if terminal {
			fmt.Fprintln(out)
		}
		if err != nil && (err != io.EOF || s == "") {
			return "", errors.New("no password was given; nothing was changed")
		}
		return strings.TrimRight(s, "\r\n"), nil
	}
	if terminal {
		if stty("-echo") == nil {
			defer stty("echo")
		}
	}
	pw, err := line("New password: ")
	if err != nil || !terminal {
		return pw, err
	}
	again, err := line("Again: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("the two passwords differ; nothing was changed")
	}
	return pw, nil
}

func stty(arg string) error {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// isTerminal reports whether stdin is a terminal rather than a pipe or file.
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
