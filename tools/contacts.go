package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

// ContactsConfig holds CardDAV contacts settings.
type ContactsConfig struct {
	Server   string
	Username string
	Password string
	Writable bool
}

var contactsOverrides sync.Map // goroutineID → *ContactsConfig

// SetContactsOverride sets the contacts config for the current goroutine.
func SetContactsOverride(cfg *ContactsConfig) {
	contactsOverrides.Store(goroutineID(), cfg)
}

// ClearContactsOverride removes the contacts config for the current goroutine.
func ClearContactsOverride() {
	contactsOverrides.Delete(goroutineID())
}

// ContactsAvailable returns true if contacts tools should be visible.
func ContactsAvailable() bool {
	_, ok := contactsOverrides.Load(goroutineID())
	return ok
}

// ContactsWritable returns true if contacts write tools should be visible.
func ContactsWritable() bool {
	v, ok := contactsOverrides.Load(goroutineID())
	if !ok {
		return false
	}
	return v.(*ContactsConfig).Writable
}

func getContactsConfig() (*ContactsConfig, error) {
	if v, ok := contactsOverrides.Load(goroutineID()); ok {
		return v.(*ContactsConfig), nil
	}
	return nil, fmt.Errorf("no contacts config for this context")
}

func dialCardDAV(cfg *ContactsConfig) (*carddav.Client, error) {
	httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, cfg.Username, cfg.Password)
	return carddav.NewClient(httpClient, cfg.Server)
}

func findAddressBooks(cfg *ContactsConfig) ([]carddav.AddressBook, error) {
	client, err := dialCardDAV(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to CardDAV: %w", err)
	}
	ctx := context.Background()
	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("find principal: %w", err)
	}
	homeSet, err := client.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("find address book home set: %w", err)
	}
	books, err := client.FindAddressBooks(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("list address books: %w", err)
	}
	return books, nil
}

func formatContact(obj carddav.AddressObject) string {
	card := obj.Card
	var sb strings.Builder

	name := card.PreferredValue(vcard.FieldFormattedName)
	if name != "" {
		sb.WriteString(name + "\n")
	}

	for _, f := range card[vcard.FieldEmail] {
		if f.Value != "" {
			sb.WriteString("  Email: " + f.Value + "\n")
		}
	}
	for _, f := range card[vcard.FieldTelephone] {
		if f.Value != "" {
			sb.WriteString("  Phone: " + f.Value + "\n")
		}
	}
	if org := card.PreferredValue(vcard.FieldOrganization); org != "" {
		sb.WriteString("  Organization: " + org + "\n")
	}
	if title := card.PreferredValue(vcard.FieldTitle); title != "" {
		sb.WriteString("  Title: " + title + "\n")
	}
	for _, f := range card[vcard.FieldAddress] {
		if f.Value != "" {
			// ADR is semicolon-separated: PO;ext;street;city;region;postal;country
			parts := strings.Split(f.Value, ";")
			var addrParts []string
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					addrParts = append(addrParts, p)
				}
			}
			if len(addrParts) > 0 {
				sb.WriteString("  Address: " + strings.Join(addrParts, ", ") + "\n")
			}
		}
	}
	if bday := card.PreferredValue(vcard.FieldBirthday); bday != "" {
		sb.WriteString("  Birthday: " + bday + "\n")
	}
	if note := card.PreferredValue(vcard.FieldNote); note != "" {
		sb.WriteString("  Note: " + note + "\n")
	}
	sb.WriteString("  Path: " + obj.Path + "\n")

	return sb.String()
}

// --- Tool executors ---

func execContactsSearch(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Query       string `json:"query"`
		AddressBook string `json:"address_book"`
		Limit       int    `json:"limit"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}

	cfg, err := getContactsConfig()
	if err != nil {
		return "", err
	}

	client, err := dialCardDAV(cfg)
	if err != nil {
		return "", err
	}
	ctx := context.Background()

	books, err := findAddressBooks(cfg)
	if err != nil {
		return "", err
	}

	var allResults []carddav.AddressObject
	var queryErrors []string
	for _, book := range books {
		if args.AddressBook != "" && book.Path != args.AddressBook {
			continue
		}
		// Try server-side filtered query first
		query := &carddav.AddressBookQuery{
			DataRequest: carddav.AddressDataRequest{AllProp: true},
			PropFilters: []carddav.PropFilter{
				{
					Name: "FN",
					TextMatches: []carddav.TextMatch{{
						Text:      args.Query,
						MatchType: carddav.MatchContains,
					}},
				},
				{
					Name: "EMAIL",
					TextMatches: []carddav.TextMatch{{
						Text:      args.Query,
						MatchType: carddav.MatchContains,
					}},
				},
				{
					Name: "TEL",
					TextMatches: []carddav.TextMatch{{
						Text:      args.Query,
						MatchType: carddav.MatchContains,
					}},
				},
			},
			FilterTest: carddav.FilterAnyOf,
		}
		results, err := client.QueryAddressBook(ctx, book.Path, query)
		if err != nil {
			// Fallback: fetch all contacts and filter client-side.
			// Some servers (e.g. Xandikos) crash on text-match filters
			// when contacts contain non-ASCII characters.
			// Use a "FN property exists" filter (no text-match) to avoid
			// the collation bug while still getting address-data back.
			log.Printf("CardDAV filtered query %s failed: %v — falling back to client-side filtering", book.Path, err)
			fallbackQuery := &carddav.AddressBookQuery{
				DataRequest: carddav.AddressDataRequest{AllProp: true},
				PropFilters: []carddav.PropFilter{
					{Name: "FN"}, // "property exists" — no text matching
				},
			}
			results, err = client.QueryAddressBook(ctx, book.Path, fallbackQuery)
			if err != nil {
				log.Printf("CardDAV fallback query %s also failed: %v", book.Path, err)
				queryErrors = append(queryErrors, fmt.Sprintf("%s: %v", book.Path, err))
				continue
			}
			log.Printf("CardDAV fallback for %s: fetched %d contacts for client-side filtering", book.Path, len(results))
		}
		allResults = append(allResults, results...)
	}

	// Go-side case-insensitive refilter
	needle := strings.ToLower(args.Query)
	filtered := allResults[:0]
	for _, obj := range allResults {
		card := obj.Card
		fn := strings.ToLower(card.PreferredValue(vcard.FieldFormattedName))
		if strings.Contains(fn, needle) {
			filtered = append(filtered, obj)
			continue
		}
		match := false
		for _, f := range card[vcard.FieldEmail] {
			if strings.Contains(strings.ToLower(f.Value), needle) {
				match = true
				break
			}
		}
		if match {
			filtered = append(filtered, obj)
			continue
		}
		for _, f := range card[vcard.FieldTelephone] {
			if strings.Contains(strings.ToLower(f.Value), needle) {
				match = true
				break
			}
		}
		if match {
			filtered = append(filtered, obj)
			continue
		}
		if strings.Contains(strings.ToLower(card.PreferredValue(vcard.FieldOrganization)), needle) {
			filtered = append(filtered, obj)
		}
	}
	allResults = filtered

	if len(allResults) > args.Limit {
		allResults = allResults[:args.Limit]
	}

	if len(allResults) == 0 {
		msg := fmt.Sprintf("No contacts matching %q.", args.Query)
		if len(queryErrors) > 0 {
			msg += "\nErrors querying address books:\n  " + strings.Join(queryErrors, "\n  ")
		}
		return msg, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d contacts matching %q:\n\n", len(allResults), args.Query))
	for _, obj := range allResults {
		sb.WriteString(formatContact(obj))
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String()), nil
}

func execContactsGet(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	cfg, err := getContactsConfig()
	if err != nil {
		return "", err
	}

	client, err := dialCardDAV(cfg)
	if err != nil {
		return "", err
	}

	obj, err := client.GetAddressObject(context.Background(), args.Path)
	if err != nil {
		return "", fmt.Errorf("get contact: %w", err)
	}

	return formatContact(*obj), nil
}

func execContactsCreate(rawArgs json.RawMessage) (string, error) {
	var args struct {
		AddressBook  string `json:"address_book"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
		Organization string `json:"organization"`
		Note         string `json:"note"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.AddressBook == "" || args.Name == "" {
		return "", fmt.Errorf("address_book and name are required")
	}

	cfg, err := getContactsConfig()
	if err != nil {
		return "", err
	}
	if !cfg.Writable {
		return "", fmt.Errorf("contacts are not writable")
	}

	uid := fmt.Sprintf("%d-%d@ai-webfetch", time.Now().UnixNano(), goroutineID())

	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, uid)
	card.SetValue(vcard.FieldFormattedName, args.Name)
	// Set N field (structured name) from formatted name
	card.SetValue(vcard.FieldName, args.Name+";;;;")
	if args.Email != "" {
		card.AddValue(vcard.FieldEmail, args.Email)
	}
	if args.Phone != "" {
		card.AddValue(vcard.FieldTelephone, args.Phone)
	}
	if args.Organization != "" {
		card.SetValue(vcard.FieldOrganization, args.Organization)
	}
	if args.Note != "" {
		card.SetValue(vcard.FieldNote, args.Note)
	}

	client, err := dialCardDAV(cfg)
	if err != nil {
		return "", err
	}

	path := strings.TrimRight(args.AddressBook, "/") + "/" + uid + ".vcf"
	obj, err := client.PutAddressObject(context.Background(), path, card)
	if err != nil {
		return "", fmt.Errorf("create contact: %w", err)
	}

	return fmt.Sprintf("Contact created: %s\nPath: %s", args.Name, obj.Path), nil
}

func execContactsUpdate(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Path         string `json:"path"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
		Organization string `json:"organization"`
		Note         string `json:"note"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	cfg, err := getContactsConfig()
	if err != nil {
		return "", err
	}
	if !cfg.Writable {
		return "", fmt.Errorf("contacts are not writable")
	}

	client, err := dialCardDAV(cfg)
	if err != nil {
		return "", err
	}
	ctx := context.Background()

	obj, err := client.GetAddressObject(ctx, args.Path)
	if err != nil {
		return "", fmt.Errorf("get contact: %w", err)
	}

	card := obj.Card
	if args.Name != "" {
		card.SetValue(vcard.FieldFormattedName, args.Name)
		card.SetValue(vcard.FieldName, args.Name+";;;;")
	}
	if args.Email != "" {
		card[vcard.FieldEmail] = []*vcard.Field{{Value: args.Email}}
	}
	if args.Phone != "" {
		card[vcard.FieldTelephone] = []*vcard.Field{{Value: args.Phone}}
	}
	if args.Organization != "" {
		card.SetValue(vcard.FieldOrganization, args.Organization)
	}
	if args.Note != "" {
		card.SetValue(vcard.FieldNote, args.Note)
	}

	updated, err := client.PutAddressObject(ctx, args.Path, card)
	if err != nil {
		return "", fmt.Errorf("update contact: %w", err)
	}

	return fmt.Sprintf("Contact updated: %s", updated.Path), nil
}

func execContactsDelete(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	cfg, err := getContactsConfig()
	if err != nil {
		return "", err
	}
	if !cfg.Writable {
		return "", fmt.Errorf("contacts are not writable")
	}

	client, err := dialCardDAV(cfg)
	if err != nil {
		return "", err
	}

	if err := client.RemoveAll(context.Background(), args.Path); err != nil {
		return "", fmt.Errorf("delete contact: %w", err)
	}

	return fmt.Sprintf("Contact deleted: %s", args.Path), nil
}

// --- Tool registration ---

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "contacts_search",
				Description: "Search contacts by name, email, or phone number. Returns matching contacts with their details.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"query":        {Type: "string", Description: "Search text (name, email, or phone)"},
						"address_book": {Type: "string", Description: "Address book path (optional, searches all if omitted)"},
						"limit":        {Type: "integer", Description: "Max results (default: 20)"},
					},
					Required: []string{"query"},
				},
			},
		},
		Execute: execContactsSearch,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "contacts_get",
				Description: "Get full details of a single contact by path. Shows all vCard fields: name, emails, phones, organization, address, birthday, notes.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Contact path from contacts_search output"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: execContactsGet,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "contacts_create",
				Description: "Create a new contact in a CardDAV address book.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"address_book":  {Type: "string", Description: "Address book path"},
						"name":          {Type: "string", Description: "Contact full name"},
						"email":         {Type: "string", Description: "Email address (optional)"},
						"phone":         {Type: "string", Description: "Phone number (optional)"},
						"organization":  {Type: "string", Description: "Organization/company (optional)"},
						"note":          {Type: "string", Description: "Note (optional)"},
					},
					Required: []string{"address_book", "name"},
				},
			},
		},
		Execute: execContactsCreate,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "contacts_update",
				Description: "Update an existing contact. Only specified fields are changed.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":          {Type: "string", Description: "Contact path (from contacts_search output)"},
						"name":          {Type: "string", Description: "New full name"},
						"email":         {Type: "string", Description: "New email address"},
						"phone":         {Type: "string", Description: "New phone number"},
						"organization":  {Type: "string", Description: "New organization"},
						"note":          {Type: "string", Description: "New note"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: execContactsUpdate,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "contacts_delete",
				Description: "Delete a contact by path.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Contact path (from contacts_search output)"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: execContactsDelete,
	})
}
