package tools

import (
	"testing"
	"time"
)

func TestNutriTimestampIsZonelessUserWallClock(t *testing.T) {
	if err := SetTimezone("Europe/Prague"); err != nil {
		t.Fatalf("SetTimezone: %v", err)
	}
	defer ClearTimezone()

	got := nutriTimestamp()

	// No zone designator: Dart's DateTime.parse must read this as local time,
	// not convert it to UTC. A trailing "Z" or "+02:00" is the bug.
	if _, err := time.Parse("2006-01-02T15:04:05", got); err != nil {
		t.Fatalf("timestamp %q is not a zoneless wall clock: %v", got, err)
	}

	// The wall clock must be Prague's, not the machine's.
	wantHour := time.Now().In(mustLoad(t, "Europe/Prague")).Hour()
	parsed, _ := time.Parse("2006-01-02T15:04:05", got)
	if parsed.Hour() != wantHour {
		t.Errorf("hour = %d, want Prague hour %d (got %q)", parsed.Hour(), wantHour, got)
	}
}

func TestSetTimezoneRejectsUnknownZone(t *testing.T) {
	if err := SetTimezone("Middle/Earth"); err == nil {
		ClearTimezone()
		t.Fatal("expected an error for an unknown zone, got nil")
	}
	// Nothing stored, so Location falls through to the process zone.
	if Location() != time.Local {
		t.Errorf("Location() = %v, want time.Local after a rejected zone", Location())
	}
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}
