package api

import (
	"net/http"
	"testing"
	"time"
)

// A wrong password and a username that does not exist must cost the same.
//
// The short-circuit this guards against is easy to reintroduce — writing
// `err != nil || !VerifyPassword(...)` is the natural way to spell it — and
// the symptom is invisible in any functional test: both cases return the same
// 401 with the same body. Only the clock tells them apart, so only the clock
// can test it.
func TestLoginSpendsTheSameWorkOnAnUnknownUsername(t *testing.T) {
	if testing.Short() {
		t.Skip("timing measurement")
	}
	srv, _, _ := mfaFixture(t)

	// Median of several attempts: a single sample on a loaded machine is
	// noise, and PBKDF2 at 210k rounds is slow enough that a handful is cheap.
	const rounds = 5
	known := medianDuration(t, rounds, func() {
		w, _ := post(t, srv.login, map[string]string{"username": "mai", "password": "wrong"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("known username with a wrong password: status %d, want 401", w.Code)
		}
	})
	unknown := medianDuration(t, rounds, func() {
		w, _ := post(t, srv.login, map[string]string{"username": "nobody", "password": "wrong"})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unknown username: status %d, want 401", w.Code)
		}
	})

	// Deliberately loose. Without the fix the ratio is on the order of 1:100,
	// so a quarter is a wide margin that still fails loudly on a regression
	// while tolerating a noisy CI box.
	if unknown < known/4 {
		t.Fatalf("unknown username answered in %v against %v for a known one — "+
			"the enumeration oracle is back", unknown, known)
	}
}

func medianDuration(t *testing.T, n int, fn func()) time.Duration {
	t.Helper()
	samples := make([]time.Duration, n)
	for i := range samples {
		start := time.Now()
		fn()
		samples[i] = time.Since(start)
	}
	// n is tiny; an insertion sort keeps this dependency-free and obvious.
	for i := 1; i < len(samples); i++ {
		for j := i; j > 0 && samples[j] < samples[j-1]; j-- {
			samples[j], samples[j-1] = samples[j-1], samples[j]
		}
	}
	return samples[len(samples)/2]
}
