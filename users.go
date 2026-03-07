package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"ai-webfetch/tools"
)

// UserChats maps message categories to Telegram chat IDs.
type UserChats struct {
	News  int64 `json:"news"`
	Mail  int64 `json:"mail"`
	Other int64 `json:"other"`
}

// UserImapConfig holds IMAP credentials for a single user.
type UserImapConfig struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// UserHAConfig controls Home Assistant access for a user.
type UserHAConfig struct {
	Enabled bool `json:"enabled"`
}

// UserCalendarConfig holds CalDAV/iCal calendar settings for a user.
type UserCalendarConfig struct {
	Server   string        `json:"server,omitempty"`
	Username string        `json:"username,omitempty"`
	Password string        `json:"password,omitempty"`
	Writable bool          `json:"writable,omitempty"`
	ICalURLs []UserICalURL `json:"ical_urls,omitempty"`
}

// UserICalURL is a read-only iCal subscription URL.
type UserICalURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// UserContactsConfig holds CardDAV contacts settings for a user.
type UserContactsConfig struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"`
	Writable bool   `json:"writable,omitempty"`
}

// UserConfig holds all per-user settings.
type UserConfig struct {
	TelegramID int64               `json:"telegram_id"`
	Language   string              `json:"language,omitempty"`
	Chats      UserChats           `json:"chats"`
	Imap       *UserImapConfig     `json:"imap,omitempty"`
	HA         *UserHAConfig       `json:"homeassistant,omitempty"`
	Calendar   *UserCalendarConfig `json:"calendar,omitempty"`
	Contacts   *UserContactsConfig `json:"contacts,omitempty"`
	MCP        map[string]bool     `json:"mcp,omitempty"`
	Memory     string              `json:"memory,omitempty"`
}

var (
	usersMap  map[string]*UserConfig
	usersOnce sync.Once
	usersPath = "users.json"
)

func loadUsers(path string) (map[string]*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var users map[string]*UserConfig
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func getUsers() map[string]*UserConfig {
	usersOnce.Do(func() {
		users, err := loadUsers(usersPath)
		if err != nil {
			return
		}
		usersMap = users
	})
	return usersMap
}

func resolveUserByTelegramID(users map[string]*UserConfig, id int64) *UserConfig {
	for _, u := range users {
		if u.TelegramID == id {
			return u
		}
	}
	return nil
}

func resolveUserByName(users map[string]*UserConfig, name string) (*UserConfig, error) {
	u, ok := users[name]
	if !ok {
		var available []string
		for k := range users {
			available = append(available, k)
		}
		return nil, fmt.Errorf("user %q not found (available: %s)", name, strings.Join(available, ", "))
	}
	return u, nil
}

// userImapConfig converts UserImapConfig to tools.ImapUserConfig.
func userImapConfig(u *UserConfig) *tools.ImapUserConfig {
	if u == nil || u.Imap == nil {
		return nil
	}
	return &tools.ImapUserConfig{
		Server:   u.Imap.Server,
		Username: u.Imap.Username,
		Password: u.Imap.Password,
	}
}

// userCalendarConfig converts UserCalendarConfig to tools.CalendarConfig.
func userCalendarConfig(u *UserConfig) *tools.CalendarConfig {
	if u == nil || u.Calendar == nil {
		return nil
	}
	c := u.Calendar
	// Need either a CalDAV server or iCal URLs
	if c.Server == "" && len(c.ICalURLs) == 0 {
		return nil
	}
	cfg := &tools.CalendarConfig{
		Server:   c.Server,
		Username: c.Username,
		Password: c.Password,
		Writable: c.Writable,
	}
	for _, u := range c.ICalURLs {
		cfg.ICalURLs = append(cfg.ICalURLs, tools.ICalURL{Name: u.Name, URL: u.URL})
	}
	return cfg
}

// userContactsConfig converts UserContactsConfig to tools.ContactsConfig.
func userContactsConfig(u *UserConfig) *tools.ContactsConfig {
	if u == nil || u.Contacts == nil {
		return nil
	}
	c := u.Contacts
	if c.Server == "" {
		return nil
	}
	return &tools.ContactsConfig{
		Server:   c.Server,
		Username: c.Username,
		Password: c.Password,
		Writable: c.Writable,
	}
}

// userChatID returns the chat ID for a message category.
// If overrideChatID is non-zero, it takes precedence.
func userChatID(u *UserConfig, category string, overrideChatID int64) int64 {
	if overrideChatID != 0 {
		return overrideChatID
	}
	if u == nil {
		return 0
	}
	switch category {
	case "news":
		return u.Chats.News
	case "mail":
		return u.Chats.Mail
	default:
		return u.Chats.Other
	}
}
