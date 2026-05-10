package auth

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHashAndVerify_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-Battery1!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword("correct-horse-Battery1!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("expected match, got false")
	}
}

func TestVerify_WrongPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-Battery1!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword("wrong-horse-Battery1!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected mismatch, got true")
	}
}

func TestVerify_CorruptHash(t *testing.T) {
	_, err := VerifyPassword("anything", "$argon2id$v=19$m=65536,t=3,p=4$!!!bad!!!$!!!bad!!!")
	if err == nil {
		t.Fatal("expected error for corrupt hash, got nil")
	}
}

func TestVerify_EmptyHash(t *testing.T) {
	_, err := VerifyPassword("anything", "")
	if err == nil {
		t.Fatal("expected error for empty hash, got nil")
	}
}

func TestHash_Deterministic_DifferentSalts(t *testing.T) {
	h1, err := HashPassword("MyP@ssword123")
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	h2, err := HashPassword("MyP@ssword123")
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of same plaintext should differ (different salts)")
	}
	if !strings.HasPrefix(h1, "$argon2id$") {
		t.Fatalf("hash does not start with PHC prefix: %q", h1)
	}
}

func TestSemaphoreLimitsConcurrency(t *testing.T) {
	// Skip in short mode — each call takes ~0.5–1s on modern hardware.
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}

	const goroutines = 20
	limit := cap(verifySem)

	hash, err := HashPassword("TestPass123!")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// verifySem is a buffered channel of size N (the limit).
	// When a goroutine is inside the protected block it holds one slot
	// (len increases by 1). We sample len(verifySem) from a background
	// goroutine to observe peak concurrency inside the protected region.
	var (
		maxObserved atomic.Int64
		stop        = make(chan struct{})
		wg          sync.WaitGroup
	)

	// Sampler — runs until all Argon2id calls complete.
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				cur := int64(len(verifySem))
				for {
					old := maxObserved.Load()
					if cur <= old || maxObserved.CompareAndSwap(old, cur) {
						break
					}
				}
			}
		}
	}()

	for range goroutines {
		wg.Go(func() {
			_, _ = VerifyPassword("TestPass123!", hash)
		})
	}
	wg.Wait()
	close(stop)

	if maxObserved.Load() > int64(limit) {
		t.Errorf("max concurrent Argon2id calls %d exceeded semaphore limit %d",
			maxObserved.Load(), limit)
	}
}

func TestValidatePasswordComplexity(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"too short", "Ab1!", true},
		{"only letters", "abcdefghijkl", true},
		{"only digits", "123456789012", true},
		{"no symbol", "abcdefghi123", true},
		{"valid 12-char all groups", "abcdefghi1!2", false},
		{"valid longer", "MySecure-Pass1word!", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordComplexity(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil for %q", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.input, err)
			}
		})
	}
}
