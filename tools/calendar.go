package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apognu/gocal"
	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// CalendarConfig holds CalDAV/iCal calendar settings.
type CalendarConfig struct {
	Server   string
	Username string
	Password string
	Writable bool
	ICalURLs []ICalURL
}

// ICalURL is a read-only iCal subscription URL.
type ICalURL struct {
	Name string
	URL  string
}

var calOverrides sync.Map // goroutineID → *CalendarConfig

// SetCalendarOverride sets the calendar config for the current goroutine.
func SetCalendarOverride(cfg *CalendarConfig) {
	calOverrides.Store(goroutineID(), cfg)
}

// ClearCalendarOverride removes the calendar config for the current goroutine.
func ClearCalendarOverride() {
	calOverrides.Delete(goroutineID())
}

// CalendarAvailable returns true if calendar tools should be visible.
func CalendarAvailable() bool {
	_, ok := calOverrides.Load(goroutineID())
	return ok
}

// CalendarWritable returns true if calendar write tools should be visible.
func CalendarWritable() bool {
	v, ok := calOverrides.Load(goroutineID())
	if !ok {
		return false
	}
	cfg := v.(*CalendarConfig)
	return cfg.Server != "" && cfg.Writable
}

func getCalendarConfig() (*CalendarConfig, error) {
	if v, ok := calOverrides.Load(goroutineID()); ok {
		return v.(*CalendarConfig), nil
	}
	return nil, fmt.Errorf("no calendar config for this context")
}

func dialCalDAV(cfg *CalendarConfig) (*caldav.Client, error) {
	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, cfg.Username, cfg.Password)
	return caldav.NewClient(httpClient, cfg.Server)
}

func findCalendars(cfg *CalendarConfig) ([]caldav.Calendar, error) {
	client, err := dialCalDAV(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to CalDAV: %w", err)
	}
	ctx := context.Background()
	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("find principal: %w", err)
	}
	homeSet, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("find calendar home set: %w", err)
	}
	calendars, err := client.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	return calendars, nil
}

// calEvent is a unified event type for formatting.
type calEvent struct {
	Summary      string
	Start        time.Time
	End          time.Time
	AllDay       bool
	Location     string
	Description  string
	Attendees    []string
	Organizer    string
	Status       string
	UID          string
	Path         string // empty for iCal subscriptions
	CalendarName string
	ReadOnly     bool
}

func fetchICalEvents(ical ICalURL, start, end time.Time) ([]calEvent, error) {
	resp, err := http.Get(ical.URL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", ical.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", ical.Name, resp.StatusCode)
	}

	parser := gocal.NewParser(resp.Body)
	parser.Start = &start
	parser.End = &end
	if err := parser.Parse(); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ical.Name, err)
	}

	var events []calEvent
	for _, e := range parser.Events {
		ev := calEvent{
			Summary:      e.Summary,
			Location:     e.Location,
			Description:  e.Description,
			Status:       e.Status,
			UID:          e.Uid,
			CalendarName: ical.Name + " [subscription]",
			ReadOnly:     true,
		}
		if e.Start != nil {
			ev.Start = *e.Start
		}
		if e.End != nil {
			ev.End = *e.End
		}
		// Detect all-day: start at midnight, end at midnight, duration = 24h multiple
		if !ev.Start.IsZero() && !ev.End.IsZero() &&
			ev.Start.Hour() == 0 && ev.Start.Minute() == 0 &&
			ev.End.Hour() == 0 && ev.End.Minute() == 0 {
			ev.AllDay = true
		}
		if e.Organizer != nil {
			ev.Organizer = e.Organizer.Cn
			if ev.Organizer == "" {
				ev.Organizer = e.Organizer.Value
			}
		}
		for _, a := range e.Attendees {
			name := a.Cn
			if name == "" {
				name = a.Value
			}
			ev.Attendees = append(ev.Attendees, name)
		}
		events = append(events, ev)
	}
	return events, nil
}

// parseCalDAVEvent extracts a calEvent from a CalDAV calendar object.
func parseCalDAVEvent(obj caldav.CalendarObject, calName string) calEvent {
	ev := calEvent{
		Path:         obj.Path,
		CalendarName: calName,
	}
	if obj.Data == nil {
		return ev
	}
	for _, comp := range obj.Data.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		if v, err := comp.Props.Text(ical.PropSummary); err == nil {
			ev.Summary = v
		}
		if v, err := comp.Props.Text(ical.PropLocation); err == nil {
			ev.Location = v
		}
		if v, err := comp.Props.Text(ical.PropDescription); err == nil {
			ev.Description = v
		}
		if v, err := comp.Props.Text(ical.PropStatus); err == nil {
			ev.Status = v
		}
		if v, err := comp.Props.Text(ical.PropUID); err == nil {
			ev.UID = v
		}
		if v, err := comp.Props.DateTime(ical.PropDateTimeStart, time.Local); err == nil {
			ev.Start = v
		}
		if v, err := comp.Props.DateTime(ical.PropDateTimeEnd, time.Local); err == nil {
			ev.End = v
		}
		// Detect all-day by VALUE=DATE parameter
		if p := comp.Props.Get(ical.PropDateTimeStart); p != nil {
			if p.Params.Get("VALUE") == "DATE" {
				ev.AllDay = true
			}
		}
		if p := comp.Props.Get(ical.PropOrganizer); p != nil {
			ev.Organizer = p.Params.Get("CN")
			if ev.Organizer == "" {
				ev.Organizer = strings.TrimPrefix(p.Value, "mailto:")
			}
		}
		for _, p := range comp.Props.Values(ical.PropAttendee) {
			name := p.Params.Get("CN")
			if name == "" {
				name = strings.TrimPrefix(p.Value, "mailto:")
			}
			ev.Attendees = append(ev.Attendees, name)
		}
		break // first VEVENT
	}
	return ev
}

func formatEventLine(ev calEvent) string {
	var sb strings.Builder
	if ev.AllDay {
		sb.WriteString(ev.Start.Format("2006-01-02") + " (all day)")
	} else {
		sb.WriteString(ev.Start.Format("2006-01-02 15:04") + "-" + ev.End.Format("15:04"))
	}
	sb.WriteString(" | " + ev.Summary)
	if ev.Location != "" {
		sb.WriteString(" | " + ev.Location)
	}
	sb.WriteString("\n  Calendar: " + ev.CalendarName)
	if ev.Path != "" {
		sb.WriteString("\n  Path: " + ev.Path)
	}
	return sb.String()
}

func formatEventDetail(ev calEvent) string {
	var sb strings.Builder
	sb.WriteString("Summary: " + ev.Summary + "\n")
	if ev.AllDay {
		sb.WriteString("Start: " + ev.Start.Format("2006-01-02") + " (all day)\n")
		sb.WriteString("End: " + ev.End.Format("2006-01-02") + "\n")
	} else {
		sb.WriteString("Start: " + ev.Start.Format("2006-01-02 15:04") + "\n")
		sb.WriteString("End: " + ev.End.Format("2006-01-02 15:04") + "\n")
	}
	if ev.Location != "" {
		sb.WriteString("Location: " + ev.Location + "\n")
	}
	if ev.Description != "" {
		sb.WriteString("Description: " + ev.Description + "\n")
	}
	if ev.Organizer != "" {
		sb.WriteString("Organizer: " + ev.Organizer + "\n")
	}
	if len(ev.Attendees) > 0 {
		sb.WriteString("Attendees: " + strings.Join(ev.Attendees, ", ") + "\n")
	}
	if ev.Status != "" {
		sb.WriteString("Status: " + ev.Status + "\n")
	}
	sb.WriteString("Calendar: " + ev.CalendarName + "\n")
	if ev.Path != "" {
		sb.WriteString("Path: " + ev.Path + "\n")
	}
	return sb.String()
}

// --- Tool executors ---

func execCalList(rawArgs json.RawMessage) (string, error) {
	cfg, err := getCalendarConfig()
	if err != nil {
		return "", err
	}

	var sb strings.Builder

	// CalDAV calendars
	if cfg.Server != "" {
		calendars, err := findCalendars(cfg)
		if err != nil {
			return "", err
		}
		for _, c := range calendars {
			sb.WriteString(c.Name)
			sb.WriteString(" | " + c.Path)
			if c.Description != "" {
				sb.WriteString(" | " + c.Description)
			}
			sb.WriteByte('\n')
		}
	}

	// iCal subscriptions
	for _, u := range cfg.ICalURLs {
		sb.WriteString(u.Name + " [subscription] | " + u.URL + "\n")
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		return "No calendars found.", nil
	}
	return result, nil
}

func execCalEvents(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Calendar  string `json:"calendar"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Search    string `json:"search"`
		Limit     int    `json:"limit"`
	}
	json.Unmarshal(rawArgs, &args)

	if args.Limit <= 0 {
		args.Limit = 50
	}

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 30)

	if args.StartDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", args.StartDate, time.Local); err == nil {
			start = t
		}
	}
	if args.EndDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", args.EndDate, time.Local); err == nil {
			end = t.AddDate(0, 0, 1) // include the end date
		}
	}

	cfg, err := getCalendarConfig()
	if err != nil {
		return "", err
	}

	var allEvents []calEvent
	var queryErrors []string

	// CalDAV events
	if cfg.Server != "" {
		calendars, err := findCalendars(cfg)
		if err != nil {
			return "", err
		}
		client, err := dialCalDAV(cfg)
		if err != nil {
			return "", err
		}
		ctx := context.Background()
		for _, cal := range calendars {
			if args.Calendar != "" && cal.Path != args.Calendar && cal.Name != args.Calendar {
				continue
			}
			query := &caldav.CalendarQuery{
				CompFilter: caldav.CompFilter{
					Name: "VCALENDAR",
					Comps: []caldav.CompFilter{{
						Name:  "VEVENT",
						Start: start,
						End:   end,
					}},
				},
			}
			objects, err := client.QueryCalendar(ctx, cal.Path, query)
			if err != nil {
				log.Printf("CalDAV query %s failed: %v", cal.Path, err)
				queryErrors = append(queryErrors, fmt.Sprintf("%s: %v", cal.Name, err))
				continue
			}
			for _, obj := range objects {
				ev := parseCalDAVEvent(obj, cal.Name)
				allEvents = append(allEvents, ev)
			}
		}
	}

	// iCal events
	for _, icalURL := range cfg.ICalURLs {
		if args.Calendar != "" && icalURL.Name != args.Calendar {
			continue
		}
		events, err := fetchICalEvents(icalURL, start, end)
		if err != nil {
			log.Printf("iCal fetch %s failed: %v", icalURL.Name, err)
			queryErrors = append(queryErrors, fmt.Sprintf("%s: %v", icalURL.Name, err))
			continue
		}
		allEvents = append(allEvents, events...)
	}

	// Filter by search text
	if args.Search != "" {
		needle := strings.ToLower(args.Search)
		filtered := allEvents[:0]
		for _, ev := range allEvents {
			if strings.Contains(strings.ToLower(ev.Summary), needle) ||
				strings.Contains(strings.ToLower(ev.Description), needle) ||
				strings.Contains(strings.ToLower(ev.Location), needle) {
				filtered = append(filtered, ev)
			}
		}
		allEvents = filtered
	}

	// Sort by start time
	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Start.Before(allEvents[j].Start)
	})

	// Limit
	if len(allEvents) > args.Limit {
		allEvents = allEvents[:args.Limit]
	}

	if len(allEvents) == 0 {
		msg := fmt.Sprintf("No events found (%s to %s).", start.Format("2006-01-02"), end.Format("2006-01-02"))
		if len(queryErrors) > 0 {
			msg += "\nErrors querying calendars:\n  " + strings.Join(queryErrors, "\n  ")
		}
		return msg, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d events (%s to %s):\n\n",
		len(allEvents), start.Format("2006-01-02"), end.Format("2006-01-02")))
	for _, ev := range allEvents {
		sb.WriteString(formatEventLine(ev))
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String()), nil
}

func execCalEvent(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	cfg, err := getCalendarConfig()
	if err != nil {
		return "", err
	}
	if cfg.Server == "" {
		return "", fmt.Errorf("no CalDAV server configured")
	}

	client, err := dialCalDAV(cfg)
	if err != nil {
		return "", err
	}

	obj, err := client.GetCalendarObject(context.Background(), args.Path)
	if err != nil {
		return "", fmt.Errorf("get event: %w", err)
	}

	ev := parseCalDAVEvent(*obj, "")
	return formatEventDetail(ev), nil
}

func execCalCreateEvent(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Calendar    string `json:"calendar"`
		Summary     string `json:"summary"`
		Start       string `json:"start"`
		End         string `json:"end"`
		Location    string `json:"location"`
		Description string `json:"description"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Calendar == "" || args.Summary == "" || args.Start == "" || args.End == "" {
		return "", fmt.Errorf("calendar, summary, start, and end are required")
	}

	cfg, err := getCalendarConfig()
	if err != nil {
		return "", err
	}
	if cfg.Server == "" || !cfg.Writable {
		return "", fmt.Errorf("calendar is not writable")
	}

	startTime, allDay, err := parseEventTime(args.Start)
	if err != nil {
		return "", fmt.Errorf("invalid start: %w", err)
	}
	endTime, _, err := parseEventTime(args.End)
	if err != nil {
		return "", fmt.Errorf("invalid end: %w", err)
	}

	uid := fmt.Sprintf("%d-%d@ai-webfetch", time.Now().UnixNano(), goroutineID())

	event := ical.NewComponent(ical.CompEvent)
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetText(ical.PropSummary, args.Summary)
	if allDay {
		event.Props.SetDate(ical.PropDateTimeStart, startTime)
		event.Props.SetDate(ical.PropDateTimeEnd, endTime)
	} else {
		event.Props.SetDateTime(ical.PropDateTimeStart, startTime)
		event.Props.SetDateTime(ical.PropDateTimeEnd, endTime)
	}
	if args.Location != "" {
		event.Props.SetText(ical.PropLocation, args.Location)
	}
	if args.Description != "" {
		event.Props.SetText(ical.PropDescription, args.Description)
	}
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())

	cal := ical.NewComponent(ical.CompCalendar)
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//ai-webfetch//EN")
	cal.Children = append(cal.Children, event)

	icalCal := &ical.Calendar{Component: cal}

	client, err := dialCalDAV(cfg)
	if err != nil {
		return "", err
	}

	path := strings.TrimRight(args.Calendar, "/") + "/" + uid + ".ics"
	obj, err := client.PutCalendarObject(context.Background(), path, icalCal)
	if err != nil {
		return "", fmt.Errorf("create event: %w", err)
	}

	return fmt.Sprintf("Event created: %s\nPath: %s", args.Summary, obj.Path), nil
}

func execCalUpdateEvent(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Path        string `json:"path"`
		Summary     string `json:"summary"`
		Start       string `json:"start"`
		End         string `json:"end"`
		Location    string `json:"location"`
		Description string `json:"description"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	cfg, err := getCalendarConfig()
	if err != nil {
		return "", err
	}
	if cfg.Server == "" || !cfg.Writable {
		return "", fmt.Errorf("calendar is not writable")
	}

	client, err := dialCalDAV(cfg)
	if err != nil {
		return "", err
	}
	ctx := context.Background()

	obj, err := client.GetCalendarObject(ctx, args.Path)
	if err != nil {
		return "", fmt.Errorf("get event: %w", err)
	}
	if obj.Data == nil {
		return "", fmt.Errorf("event has no data")
	}

	// Find and modify the VEVENT component
	for _, comp := range obj.Data.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		if args.Summary != "" {
			comp.Props.SetText(ical.PropSummary, args.Summary)
		}
		if args.Start != "" {
			t, allDay, err := parseEventTime(args.Start)
			if err != nil {
				return "", fmt.Errorf("invalid start: %w", err)
			}
			if allDay {
				comp.Props.SetDate(ical.PropDateTimeStart, t)
			} else {
				comp.Props.SetDateTime(ical.PropDateTimeStart, t)
			}
		}
		if args.End != "" {
			t, allDay, err := parseEventTime(args.End)
			if err != nil {
				return "", fmt.Errorf("invalid end: %w", err)
			}
			if allDay {
				comp.Props.SetDate(ical.PropDateTimeEnd, t)
			} else {
				comp.Props.SetDateTime(ical.PropDateTimeEnd, t)
			}
		}
		if args.Location != "" {
			comp.Props.SetText(ical.PropLocation, args.Location)
		}
		if args.Description != "" {
			comp.Props.SetText(ical.PropDescription, args.Description)
		}
		break
	}

	updated, err := client.PutCalendarObject(ctx, args.Path, obj.Data)
	if err != nil {
		return "", fmt.Errorf("update event: %w", err)
	}

	return fmt.Sprintf("Event updated: %s", updated.Path), nil
}

func execCalDeleteEvent(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	cfg, err := getCalendarConfig()
	if err != nil {
		return "", err
	}
	if cfg.Server == "" || !cfg.Writable {
		return "", fmt.Errorf("calendar is not writable")
	}

	client, err := dialCalDAV(cfg)
	if err != nil {
		return "", err
	}

	if err := client.RemoveAll(context.Background(), args.Path); err != nil {
		return "", fmt.Errorf("delete event: %w", err)
	}

	return fmt.Sprintf("Event deleted: %s", args.Path), nil
}

// parseEventTime parses RFC3339 or YYYY-MM-DD. Returns (time, allDay, error).
func parseEventTime(s string) (time.Time, bool, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("expected RFC3339 or YYYY-MM-DD, got %q", s)
}

// --- Tool registration ---

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "cal_list",
				Description: "List all available calendars (CalDAV calendars and iCal subscriptions). Shows name, path, and description.",
				Parameters: Parameters{
					Type:       "object",
					Properties: map[string]Property{},
				},
			},
		},
		Execute: execCalList,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "cal_events",
				Description: "List events from calendars within a date range. Merges events from CalDAV and iCal subscriptions. Sorted by start time.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"calendar":   {Type: "string", Description: "Calendar path or iCal subscription name (optional, all calendars if omitted)"},
						"start_date": {Type: "string", Description: "Start date YYYY-MM-DD (default: today)"},
						"end_date":   {Type: "string", Description: "End date YYYY-MM-DD (default: +30 days)"},
						"search":     {Type: "string", Description: "Filter by text in summary/description/location (case-insensitive)"},
						"limit":      {Type: "integer", Description: "Max events to return (default: 50)"},
					},
				},
			},
		},
		Execute: execCalEvents,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "cal_event",
				Description: "Get full details of a single CalDAV event by path. Shows summary, start/end, location, description, attendees, organizer, status.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Event path from cal_events output"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: execCalEvent,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "cal_create_event",
				Description: "Create a new calendar event. Use YYYY-MM-DD for all-day events or RFC3339 for timed events.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"calendar":    {Type: "string", Description: "Calendar path (from cal_list)"},
						"summary":     {Type: "string", Description: "Event title"},
						"start":       {Type: "string", Description: "Start time (RFC3339 e.g. 2026-03-10T14:00:00+01:00) or date (YYYY-MM-DD for all-day)"},
						"end":         {Type: "string", Description: "End time (RFC3339) or date (YYYY-MM-DD for all-day)"},
						"location":    {Type: "string", Description: "Event location (optional)"},
						"description": {Type: "string", Description: "Event description (optional)"},
					},
					Required: []string{"calendar", "summary", "start", "end"},
				},
			},
		},
		Execute: execCalCreateEvent,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "cal_update_event",
				Description: "Update an existing CalDAV event. Only specified fields are changed.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":        {Type: "string", Description: "Event path (from cal_events output)"},
						"summary":     {Type: "string", Description: "New event title"},
						"start":       {Type: "string", Description: "New start time (RFC3339 or YYYY-MM-DD)"},
						"end":         {Type: "string", Description: "New end time (RFC3339 or YYYY-MM-DD)"},
						"location":    {Type: "string", Description: "New location"},
						"description": {Type: "string", Description: "New description"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: execCalUpdateEvent,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "cal_delete_event",
				Description: "Delete a CalDAV event by path.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Event path (from cal_events output)"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: execCalDeleteEvent,
	})
}
