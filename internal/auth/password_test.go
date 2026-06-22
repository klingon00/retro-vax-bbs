package auth

import "testing"

func TestHashAndVerifyPassword_CorrectPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	ok, err := VerifyPassword("correct-horse-battery-staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("VerifyPassword returned false for the correct password")
	}
}

func TestHashAndVerifyPassword_WrongPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	ok, err := VerifyPassword("wrong-password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned unexpected error on a wrong password: %v", err)
	}
	if ok {
		t.Fatal("VerifyPassword returned true for an incorrect password")
	}
}

func TestVerifyPassword_GarbageHash(t *testing.T) {
	ok, err := VerifyPassword("anything", "not-a-real-hash")
	if err == nil {
		t.Fatal("expected an error for a malformed hash string, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for a malformed hash string")
	}
}

func TestHashAndVerifyPassword_EmptyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	ok, err := VerifyPassword("", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned unexpected error: %v", err)
	}
	if ok {
		t.Fatal("VerifyPassword returned true for an empty password against a real hash")
	}
}

func TestHashPassword_ProducesDifferentSaltsEachTime(t *testing.T) {
	hash1, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	hash2, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if hash1 == hash2 {
		t.Fatal("two hashes of the same password produced identical output — salt is not being randomized")
	}

	// Both should still independently verify correctly despite differing.
	for _, h := range []string{hash1, hash2} {
		ok, err := VerifyPassword("same-password", h)
		if err != nil || !ok {
			t.Fatalf("hash %q failed to verify: ok=%v err=%v", h, ok, err)
		}
	}
}
