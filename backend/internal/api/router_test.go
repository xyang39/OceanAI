package api

import (
	"testing"
	"time"
)

func TestTenantLocalDates(t *testing.T) {
	for _, tc := range []struct{ name, instant, zone, local, date string }{
		{"Sydney summer", "2026-01-10T13:30:00Z", "Australia/Sydney", "2026-01-11 00:30", "2026-01-11"},
		{"Perth summer", "2026-01-10T13:30:00Z", "Australia/Perth", "2026-01-10 21:30", "2026-01-10"},
		{"Sydney winter", "2026-07-10T13:30:00Z", "Australia/Sydney", "2026-07-10 23:30", "2026-07-10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			local, date, err := localIssueDate(instant, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			if local != tc.local || date != tc.date {
				t.Fatalf("got %q / %q, want %q / %q", local, date, tc.local, tc.date)
			}
		})
	}
}
