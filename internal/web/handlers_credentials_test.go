package web

import (
	"context"
	"log/slog"
	"net/url"
	"sync/atomic"
	"testing"

	"efb-connector/internal/efb"
)

// countingEFB records the session-bound calls made through it, so a test can
// tell which EFBProvider instance a handler actually used.
type countingEFB struct {
	efb.EFBProvider
	validates atomic.Int32
	consents  atomic.Int32
}

func (c *countingEFB) ValidateCredentials(ctx context.Context, username, password string) error {
	c.validates.Add(1)
	return c.EFBProvider.ValidateCredentials(ctx, username, password)
}

func (c *countingEFB) CheckConsentGate(ctx context.Context) (bool, error) {
	c.consents.Add(1)
	return c.EFBProvider.CheckConsentGate(ctx)
}

// Saving EFB credentials logs in as that user and then asks EFB about the
// consent gate on the same session. Both calls must go to a client created
// for this request — a shared client would let concurrent saves overwrite
// each other's login and report another user's consent state.
func TestEFBSettingsSave_UsesPerRequestSession(t *testing.T) {
	h := newTestHarness(t)
	uid := loginAs(t, h, "efbsave@example.com")

	shared := &countingEFB{EFBProvider: h.server.efb}
	h.server.efb = shared

	var sessions atomic.Int32
	perRequest := &countingEFB{EFBProvider: func() efb.EFBProvider {
		m := efb.NewMockEFBProvider(slog.Default())
		m.SetConsentGate(true)
		return m
	}()}
	h.server.newEFBSession = func() efb.EFBProvider {
		sessions.Add(1)
		return perRequest
	}

	postForm(t, h, "/settings/efb", url.Values{"username": {"u"}, "password": {"p"}})

	if n := sessions.Load(); n != 1 {
		t.Fatalf("newEFBSession called %d times, want 1", n)
	}
	if v, c := shared.validates.Load(), shared.consents.Load(); v != 0 || c != 0 {
		t.Errorf("shared client used: %d validate, %d consent-check calls, want none", v, c)
	}
	if v, c := perRequest.validates.Load(), perRequest.consents.Load(); v != 1 || c != 1 {
		t.Errorf("per-request client: %d validate, %d consent-check calls, want 1 each", v, c)
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
