// Command adduser creates a user directly in the database, with a
// properly argon2id-hashed password. This is the entire account-creation
// mechanism for `closed` registration mode until the lobby's NEW/APPROVE
// command flow exists. Deliberately a separate binary — it has no
// business being reachable from a running server process.
//
// Usage:
//
//	go run ./cmd/adduser -username alice -password 'some-strong-password'
//	go run ./cmd/adduser -username sysop -password 'admin-password' -role admin
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/klingon00/retro-vax-bbs/internal/auth"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

const dbPath = "data/retro-vax-bbs.db"

func main() {
	username := flag.String("username", "", "username for the new account (required)")
	password := flag.String("password", "", "password for the new account (required)")
	role := flag.String("role", "user", "account role: user or admin")
	flag.Parse()

	if *username == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "usage: adduser -username NAME -password PASSWORD [-role user|admin]")
		os.Exit(1)
	}
	if *role != "user" && *role != "admin" {
		fmt.Fprintf(os.Stderr, "invalid role %q: must be 'user' or 'admin'\n", *role)
		os.Exit(1)
	}
	// "new" is the self-registration routing sentinel (the public listener
	// sends username "new" to registration, not login), so an account named
	// "new" could never log in. Reject it here as BOOTSTRAP_ADMIN_USERNAME
	// already does in cmd/server. Audit 2026-07-05 #5.
	if strings.EqualFold(*username, "new") {
		fmt.Fprintln(os.Stderr, `invalid username: "new" is reserved for self-registration and could never log in`)
		os.Exit(1)
	}

	hash, err := auth.HashPassword(*password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hashing password: %v\n", err)
		os.Exit(1)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening database: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	user, err := s.CreateUser(*username, hash, *role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating user: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created user %q (id=%d, status=%s, role=%s)\n",
		user.Username, user.ID, user.Status, user.Role)
}
