package web

import (
	"log/slog"
	"net/url"
	"testing"

	"efb-connector/internal/efb"
)

// Saving EFB credentials logs in as that user and then asks EFB about the
// consent gate on the same session. Both calls must go to a client created
// for this request — a shared client would let concurrent saves overwrite
// each other's login and report another user's consent state.
func TestEFBSettingsSave_UsesPerRequestSession(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "efbsave@example.com")

	sessions := 0
	h.server.newEFBSession = func() efb.EFBProvider {
		sessions++
		m := efb.NewMockEFBProvider(slog.Default())
		m.SetConsentGate(true)
		return m
	}

	postForm(t, h, "/settings/efb", url.Values{"username": {"u"}, "password": {"p"}})

	if sessions != 1 {
		t.Fatalf("newEFBSession called %d times, want 1", sessions)
	}
	// The shared provider has no consent gate; only the per-request
	// client does. Seeing the flag proves the check used that client.
	required, _, err := h.db.GetEFBConsentState(uid)
	if err != nil {
		t.Fatalf("GetEFBConsentState: %v", err)
	}
	if !required {
		t.Error("consent_required not set: consent check did not use the per-request session")
	}
}
