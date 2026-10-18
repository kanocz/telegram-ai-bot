package tools

import (
	"sync"
	"time"
)

// The bot service usually runs in UTC while the user lives somewhere else, so
// time.Local is the wrong answer for anything the user reads: meal timestamps,
// "today" in a diary, the current time handed to the model. The zone travels
// per goroutine, like the other per-session settings.

var tzOverrides sync.Map // goroutineID → *time.Location

// SetTimezone sets the user's zone for the current goroutine from an IANA name
// such as "Europe/Prague". An unknown name is reported and nothing is stored,
// so the caller can complain instead of silently serving server time.
func SetTimezone(name string) error {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return err
	}
	tzOverrides.Store(goroutineID(), loc)
	return nil
}

// ClearTimezone removes the zone for the current goroutine.
func ClearTimezone() { tzOverrides.Delete(goroutineID()) }

// Location returns the user's zone. Sources, in order: the "timezone" field in
// users.json, the userinfo keys "nutricalc_timezone", "timezone" or "tz", and
// finally the process zone.
func Location() *time.Location {
	if v, ok := tzOverrides.Load(goroutineID()); ok {
		return v.(*time.Location)
	}
	if cfg := getUserInfoConfig(); cfg != nil {
		if entries, err := userInfoGet(cfg); err == nil {
			for _, key := range []string{"nutricalc_timezone", "timezone", "tz"} {
				e, ok := entries[key]
				if !ok || e.Value == "" {
					continue
				}
				if loc, err := time.LoadLocation(e.Value); err == nil {
					return loc
				}
			}
		}
	}
	return time.Local
}
