// Command adduser creates a user directly in the database, with a
// properly argon2id-hashed password. This is the entire account-creation
// mechanism for `closed` registration mode (per the design doc: "admin
// creates every account directly; no self-service at all") until the
// lobby's NEW/APPROVE command flow exists. It's deliberately a separate
// binary rather than a server flag — it touches the database directly
// and has no business being reachable from a running server process.
//
// Usage:
//
//	go run ./cmd/adduser -username alice -password 'some-strong-password'
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/klingon00/retro-vax-bbs/internal/auth"
	"github.com/klingon00/retro-vax-bbs/internal/store"
)

const dbPath = "data/retro-vax-bbs.db"

func main() {
	username := flag.String("username", "", "username for the new account (required)")
	password := flag.String("password", "", "password for the new account (required)")
	flag.Parse()

	if *username == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "usage: adduser -username NAME -password PASSWORD")
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

	user, err := s.CreateUser(*username, hash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating user: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created user %q (id=%d, status=%s, role=%s)\n", user.Username, user.ID, user.Status, user.Role)
}
