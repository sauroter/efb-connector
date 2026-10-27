package garmin

import (
	"context"
	"sync"
	"testing"
	"time"
)

// In dev mode one mock serves the web handlers and every sync worker at
// once. Run with -race: without the mutex the shared lastOpts and
// mfaPending writes are reported (or crash with "concurrent map writes").
func TestMockGarminProvider_ConcurrentUse(t *testing.T) {
	m := NewMockGarminProvider()
	m.SimulateMFA = true
	ctx := context.Background()
	end := time.Now()
	start := end.AddDate(0, 0, -7)

	var wg sync.WaitGroup
	for i := range 8 {
		uid := int64(i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				if _, _, err := m.ListActivities(ctx, GarminCredentials{}, start, end, ListOptions{MatchByName: true}); err != nil {
					t.Errorf("ListActivities: %v", err)
					return
				}
				_ = m.LastListOptions()
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				if _, err := m.ValidateWithMFA(ctx, uid, GarminCredentials{}); err != nil {
					t.Errorf("ValidateWithMFA: %v", err)
					return
				}
				if !m.HasMFASession(uid) {
					t.Errorf("HasMFASession(%d) = false right after ValidateWithMFA", uid)
					return
				}
				if err := m.CompleteMFA(uid, "123456"); err != nil {
					t.Errorf("CompleteMFA: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if !m.LastListOptions().MatchByName {
		t.Error("LastListOptions().MatchByName = false, want true")
	}
}

// ListActivities honours [start, end) like the real provider; the raw
// listing is unfiltered.
func TestMockGarminProvider_HonoursWindow(t *testing.T) {
	start := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
	m := &MockGarminProvider{Activities: []Activity{
		{ProviderID: "before", Date: day(9)},
		{ProviderID: "first-day", Date: day(10)},
		{ProviderID: "inside", Date: day(15)},
		{ProviderID: "at-end", Date: day(20)},
	}}

	got, _, err := m.ListActivities(context.Background(), GarminCredentials{}, start, end, ListOptions{})
	if err != nil {
		t.Fatalf("ListActivities: %v", err)
	}
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ProviderID)
	}
	if len(ids) != 2 || ids[0] != "first-day" || ids[1] != "inside" {
		t.Errorf("ListActivities = %v, want [first-day inside]", ids)
	}

	raw, _, err := m.ListActivitiesRaw(context.Background(), GarminCredentials{}, 30)
	if err != nil {
		t.Fatalf("ListActivitiesRaw: %v", err)
	}
	if len(raw) != 4 {
		t.Errorf("ListActivitiesRaw returned %d activities, want all 4", len(raw))
	}
}
