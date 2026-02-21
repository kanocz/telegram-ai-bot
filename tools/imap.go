package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"strings"
	"time"

	netmail "net/mail"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"

	// Register charset decoders (windows-1252, iso-8859-*, koi8-r, etc.)
	_ "github.com/emersion/go-message/charset"
)

type imapConfig struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"`
}

var imapCfg *imapConfig

func getImapConfig() (*imapConfig, error) {
	if imapCfg != nil {
		return imapCfg, nil
	}
	data, err := os.ReadFile("imap.json")
	if err != nil {
		return nil, fmt.Errorf("cannot read imap.json: %w", err)
	}
	var cfg imapConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid imap.json: %w", err)
	}
	imapCfg = &cfg
	return imapCfg, nil
}

func dialIMAP() (*imapclient.Client, error) {
	cfg, err := getImapConfig()
	if err != nil {
		return nil, err
	}
	c, err := imapclient.DialTLS(cfg.Server, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s failed: %w", cfg.Server, err)
	}
	if err := c.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("login failed: %w", err)
	}
	return c, nil
}

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "imap_list_mailboxes",
				Description: "List all mailboxes (folders) in the email account.",
				Parameters: Parameters{
					Type:       "object",
					Properties: map[string]Property{},
				},
			},
		},
		Execute: execListMailboxes,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "imap_list_messages",
				Description: "List messages in a mailbox. Returns UID, subject, sender, date, and flags. All filters can be combined (AND logic). Partial string matching for from/to/subject/text/body.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"mailbox":     {Type: "string", Description: "Mailbox name, e.g. INBOX, Sent (default: INBOX)"},
						"limit":       {Type: "integer", Description: "Max number of messages to return, 1-50 (default: 20)"},
						"since_hours": {Type: "number", Description: "Messages from the last N hours (e.g. 24 for last day, 0.5 for last 30 min)"},
						"unseen":      {Type: "boolean", Description: "Only unread messages (default: false)"},
						"from":        {Type: "string", Description: "Filter by sender (partial match, e.g. \"john\" or \"@gmail.com\")"},
						"to":          {Type: "string", Description: "Filter by recipient (partial match)"},
						"participant": {Type: "string", Description: "Filter by sender OR recipient (partial match) — finds all mail involving a person"},
						"subject":     {Type: "string", Description: "Filter by subject (partial match)"},
						"body":        {Type: "string", Description: "Search in message body text"},
						"text":        {Type: "string", Description: "Search in entire message (headers + body)"},
					},
				},
			},
		},
		Execute: execListMessages,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "imap_read_message",
				Description: "Read full email content by its UID. Does not mark the message as read. For batch processing of multiple emails, prefer imap_summarize_message to save context.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"mailbox":    {Type: "string", Description: "Mailbox name (default: INBOX)"},
						"uid":        {Type: "integer", Description: "Message UID from imap_list_messages"},
						"no_headers": {Type: "boolean", Description: "Skip email headers, return body only (default: false)"},
						"max_length": {Type: "integer", Description: "Truncate output to N characters (default: unlimited)"},
					},
					Required: []string{"uid"},
				},
			},
		},
		Execute: execReadMessage,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "imap_summarize_message",
				Description: "Fetch an email by UID and return a concise AI-generated summary. Uses a sub-agent with a separate context, so the full email body does not consume the main conversation context. Ideal for batch-summarizing multiple emails.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"mailbox": {Type: "string", Description: "Mailbox name (default: INBOX)"},
						"uid":     {Type: "integer", Description: "Message UID from imap_list_messages"},
						"prompt":  {Type: "string", Description: "Custom summarization instruction (optional)"},
					},
					Required: []string{"uid"},
				},
			},
		},
		Execute: execSummarizeMessage,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "imap_digest_message",
				Description: "All-in-one email analysis via sub-agent: fetches the email, searches for conversation history with the sender in INBOX and Sent (last N days), then produces a summary, category, and conversation context. Everything runs in a separate context to save the main conversation window.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"mailbox":       {Type: "string", Description: "Mailbox name (default: INBOX)"},
						"uid":           {Type: "integer", Description: "Message UID from imap_list_messages"},
						"context_hours": {Type: "number", Description: "How far back to search for conversation history in hours (default: 336 = 14 days)"},
						"sent_mailbox":  {Type: "string", Description: "Name of the Sent folder (default: Sent)"},
					},
					Required: []string{"uid"},
				},
			},
		},
		Execute: execDigestMessage,
	})
}

func execListMailboxes(rawArgs json.RawMessage) (string, error) {
	c, err := dialIMAP()
	if err != nil {
		return "", err
	}
	defer c.Close()

	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return "", fmt.Errorf("LIST failed: %w", err)
	}

	var sb strings.Builder
	for _, b := range boxes {
		sb.WriteString(b.Mailbox)
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func execListMessages(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Mailbox    string  `json:"mailbox"`
		Limit      int     `json:"limit"`
		SinceHours float64 `json:"since_hours"`
		Unseen     bool    `json:"unseen"`
		From        string  `json:"from"`
		To          string  `json:"to"`
		Participant string  `json:"participant"`
		Subject     string  `json:"subject"`
		Body       string  `json:"body"`
		Text       string  `json:"text"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Mailbox == "" {
		args.Mailbox = "INBOX"
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	if args.Limit > 50 {
		args.Limit = 50
	}

	c, err := dialIMAP()
	if err != nil {
		return "", err
	}
	defer c.Close()

	sel, err := c.Select(args.Mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return "", fmt.Errorf("SELECT %s failed: %w", args.Mailbox, err)
	}
	if sel.NumMessages == 0 {
		return "Mailbox is empty.", nil
	}

	fetchOpts := &imap.FetchOptions{
		Envelope: true,
		Flags:    true,
		UID:      true,
	}

	var msgs []*imapclient.FetchMessageBuffer
	useSearch := args.SinceHours > 0 || args.Unseen ||
		args.From != "" || args.To != "" || args.Participant != "" ||
		args.Subject != "" || args.Body != "" || args.Text != ""

	if useSearch {
		criteria := &imap.SearchCriteria{}

		if args.Unseen {
			criteria.NotFlag = []imap.Flag{imap.FlagSeen}
		}
		if args.From != "" {
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: args.From})
		}
		if args.To != "" {
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "To", Value: args.To})
		}
		if args.Participant != "" {
			// OR(FROM participant, TO participant)
			criteria.Or = append(criteria.Or, [2]imap.SearchCriteria{
				{Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: args.Participant}}},
				{Header: []imap.SearchCriteriaHeaderField{{Key: "To", Value: args.Participant}}},
			})
		}
		if args.Subject != "" {
			criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: args.Subject})
		}
		if args.Body != "" {
			criteria.Body = []string{args.Body}
		}
		if args.Text != "" {
			criteria.Text = []string{args.Text}
		}

		var cutoff time.Time
		if args.SinceHours > 0 {
			cutoff = time.Now().Add(-time.Duration(args.SinceHours * float64(time.Hour)))
			searchDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
			criteria.Since = searchDay
		}

		searchData, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return "", fmt.Errorf("SEARCH failed: %w", err)
		}
		uids := searchData.AllUIDs()
		if len(uids) == 0 {
			return "No messages matching the criteria.", nil
		}

		var uidSet imap.UIDSet
		uidSet.AddNum(uids...)
		msgs, err = c.Fetch(uidSet, fetchOpts).Collect()
		if err != nil {
			return "", fmt.Errorf("FETCH failed: %w", err)
		}

		// Client-side filter by exact cutoff time (IMAP SINCE is day-level)
		if args.SinceHours > 0 {
			filtered := msgs[:0]
			for _, m := range msgs {
				if m.Envelope != nil && !m.Envelope.Date.Before(cutoff) {
					filtered = append(filtered, m)
				}
			}
			msgs = filtered
		}

		if len(msgs) == 0 {
			return "No messages matching the criteria.", nil
		}
	} else {
		// Fetch last N messages by sequence number
		from := uint32(1)
		to := sel.NumMessages
		if to > uint32(args.Limit) {
			from = to - uint32(args.Limit) + 1
		}

		var seqSet imap.SeqSet
		seqSet.AddRange(from, to)
		msgs, err = c.Fetch(seqSet, fetchOpts).Collect()
		if err != nil {
			return "", fmt.Errorf("FETCH failed: %w", err)
		}
	}

	// Apply limit cap
	if len(msgs) > args.Limit {
		msgs = msgs[len(msgs)-args.Limit:]
	}

	// Client-side post-filtering on decoded envelope values
	// (catches non-ASCII matches that IMAP SEARCH may miss)
	if args.From != "" {
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Envelope != nil && addrMatchesFilter(m.Envelope.From, args.From) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}
	if args.To != "" {
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Envelope != nil && addrMatchesFilter(m.Envelope.To, args.To) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}
	if args.Participant != "" {
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Envelope != nil &&
				(addrMatchesFilter(m.Envelope.From, args.Participant) ||
					addrMatchesFilter(m.Envelope.To, args.Participant)) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}
	if args.Subject != "" {
		needle := strings.ToLower(args.Subject)
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Envelope != nil &&
				strings.Contains(strings.ToLower(decodeHeader(m.Envelope.Subject)), needle) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}

	if len(msgs) == 0 {
		return "No messages matching the criteria.", nil
	}

	var sb strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		sb.WriteString(fmt.Sprintf("UID: %d\n", m.UID))
		if e := m.Envelope; e != nil {
			sb.WriteString(fmt.Sprintf("Date: %s\n", e.Date.Format(time.RFC3339)))
			if len(e.From) > 0 {
				sb.WriteString(fmt.Sprintf("From: %s\n", fmtImapAddrs(e.From)))
			}
			sb.WriteString(fmt.Sprintf("Subject: %s\n", decodeHeader(e.Subject)))
		}
		if len(m.Flags) > 0 {
			fs := make([]string, len(m.Flags))
			for j, f := range m.Flags {
				fs[j] = string(f)
			}
			sb.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(fs, ", ")))
		}
		sb.WriteString("---\n")
	}
	return sb.String(), nil
}

// emailContent holds parsed email data.
type emailContent struct {
	Date     string
	From     string
	FromAddr string // just the email address, for lookups
	To       string
	Cc       string
	Subject  string
	Body     string
}

// fetchEmailContent fetches and parses an email by UID (read-only, no flags changed).
func fetchEmailContent(mailbox string, uid uint32) (*emailContent, error) {
	c, err := dialIMAP()
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("SELECT %s failed: %w", mailbox, err)
	}

	bodySection := &imap.FetchItemBodySection{Peek: true}
	fetchOpts := &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}

	var uidSet imap.UIDSet
	uidSet.AddNum(imap.UID(uid))

	fetchCmd := c.Fetch(uidSet, fetchOpts)
	msgData := fetchCmd.Next()
	if msgData == nil {
		fetchCmd.Close()
		return nil, fmt.Errorf("message UID %d not found", uid)
	}

	result := &emailContent{}
	for {
		item := msgData.Next()
		if item == nil {
			break
		}
		body, ok := item.(imapclient.FetchItemDataBodySection)
		if !ok {
			continue
		}

		// Buffer raw data so we can retry on parse failure
		rawBytes, err := io.ReadAll(body.Literal)
		if err != nil || len(rawBytes) == 0 {
			continue
		}

		mr, err := mail.CreateReader(bytes.NewReader(rawBytes))
		if err != nil {
			result.Body = string(rawBytes)
			continue
		}

		if date, err := mr.Header.Date(); err == nil {
			result.Date = date.Format(time.RFC3339)
		}
		if from, err := mr.Header.AddressList("From"); err == nil {
			result.From = fmtMailAddrs(from)
			if len(from) > 0 {
				result.FromAddr = from[0].Address
			}
		}
		if result.From == "" {
			result.From = decodeHeader(mr.Header.Get("From"))
		}
		if to, err := mr.Header.AddressList("To"); err == nil {
			result.To = fmtMailAddrs(to)
		}
		if cc, err := mr.Header.AddressList("Cc"); err == nil && len(cc) > 0 {
			result.Cc = fmtMailAddrs(cc)
		}
		if subject, err := mr.Header.Subject(); err == nil {
			result.Subject = subject
		}
		if result.Subject == "" {
			result.Subject = decodeHeader(mr.Header.Get("Subject"))
		}

		var plainText, htmlText string
		var attachments []string
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			switch h := p.Header.(type) {
			case *mail.InlineHeader:
				ct, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
				b, readErr := io.ReadAll(p.Body)
				if readErr != nil {
					continue
				}
				switch ct {
				case "text/html":
					htmlText = string(b)
				default:
					plainText = string(b)
				}
			case *mail.AttachmentHeader:
				name, _ := h.Filename()
				attachments = append(attachments, name)
			}
		}

		// Prefer HTML→Markdown over plain text
		var bodySB strings.Builder
		if htmlText != "" {
			md, err := htmltomarkdown.ConvertString(htmlText)
			if err == nil {
				bodySB.WriteString(strings.TrimSpace(md))
			} else {
				bodySB.WriteString(htmlText)
			}
		} else if plainText != "" {
			bodySB.WriteString(strings.TrimSpace(plainText))
		}
		for _, name := range attachments {
			bodySB.WriteString(fmt.Sprintf("\n[Attachment: %s]", name))
		}
		result.Body = bodySB.String()

		// Fallback: if body is still empty, extract from raw message
		if result.Body == "" {
			if idx := bytes.Index(rawBytes, []byte("\r\n\r\n")); idx >= 0 {
				result.Body = strings.TrimSpace(string(rawBytes[idx+4:]))
			} else if idx := bytes.Index(rawBytes, []byte("\n\n")); idx >= 0 {
				result.Body = strings.TrimSpace(string(rawBytes[idx+2:]))
			}
		}
	}

	if err := fetchCmd.Close(); err != nil {
		return result, fmt.Errorf("FETCH failed: %w", err)
	}
	return result, nil
}

func execReadMessage(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Mailbox   string `json:"mailbox"`
		UID       uint32 `json:"uid"`
		NoHeaders bool   `json:"no_headers"`
		MaxLength int    `json:"max_length"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Mailbox == "" {
		args.Mailbox = "INBOX"
	}
	if args.UID == 0 {
		return "", fmt.Errorf("uid is required")
	}

	email, err := fetchEmailContent(args.Mailbox, args.UID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	if !args.NoHeaders {
		if email.Date != "" {
			sb.WriteString("Date: " + email.Date + "\n")
		}
		if email.From != "" {
			sb.WriteString("From: " + email.From + "\n")
		}
		if email.To != "" {
			sb.WriteString("To: " + email.To + "\n")
		}
		if email.Cc != "" {
			sb.WriteString("Cc: " + email.Cc + "\n")
		}
		if email.Subject != "" {
			sb.WriteString("Subject: " + email.Subject + "\n")
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(email.Body)

	result := sb.String()
	if args.MaxLength > 0 && len(result) > args.MaxLength {
		result = result[:args.MaxLength] + "\n[...truncated]"
	}
	return result, nil
}

func execSummarizeMessage(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Mailbox string `json:"mailbox"`
		UID     uint32 `json:"uid"`
		Prompt  string `json:"prompt"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Mailbox == "" {
		args.Mailbox = "INBOX"
	}
	if args.UID == 0 {
		return "", fmt.Errorf("uid is required")
	}
	if SubAgentFn == nil {
		return "", fmt.Errorf("sub-agent not available")
	}

	email, err := fetchEmailContent(args.Mailbox, args.UID)
	if err != nil {
		return "", err
	}

	systemPrompt := ImapSummarizePrompt
	if systemPrompt == "" {
		systemPrompt = "Summarize the following email concisely in 2-3 sentences. Focus on the main topic, key information, and any action items. Respond in the same language as the email content."
	}
	if args.Prompt != "" {
		systemPrompt = args.Prompt
	}

	content := fmt.Sprintf("From: %s\nSubject: %s\nDate: %s\n\n%s",
		email.From, email.Subject, email.Date, email.Body)

	// Truncate for sub-agent context safety
	if len(content) > 60000 {
		content = content[:60000] + "\n[...truncated]"
	}

	summary, err := SubAgentFn(systemPrompt, content)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}

	return fmt.Sprintf("From: %s\nSubject: %s\nDate: %s\nSummary: %s",
		email.From, email.Subject, email.Date, summary), nil
}

// searchRelatedMessages searches a mailbox for messages involving a participant within a time window.
func searchRelatedMessages(mailbox, participant string, sinceHours float64, limit int) ([]RelatedMsg, error) {
	c, err := dialIMAP()
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, nil // mailbox doesn't exist — not an error, just no results
	}

	criteria := &imap.SearchCriteria{}
	criteria.Or = append(criteria.Or, [2]imap.SearchCriteria{
		{Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: participant}}},
		{Header: []imap.SearchCriteriaHeaderField{{Key: "To", Value: participant}}},
	})

	var cutoff time.Time
	if sinceHours > 0 {
		cutoff = time.Now().Add(-time.Duration(sinceHours * float64(time.Hour)))
		searchDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
		criteria.Since = searchDay
	}

	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, nil
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}

	var uidSet imap.UIDSet
	uidSet.AddNum(uids...)

	fetchOpts := &imap.FetchOptions{Envelope: true, UID: true}
	msgs, err := c.Fetch(uidSet, fetchOpts).Collect()
	if err != nil {
		return nil, nil
	}

	// Client-side time filter (IMAP SINCE is day-level)
	if sinceHours > 0 {
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Envelope != nil && !m.Envelope.Date.Before(cutoff) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}

	// Client-side participant filter on decoded values
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.Envelope != nil &&
			(addrMatchesFilter(m.Envelope.From, participant) ||
				addrMatchesFilter(m.Envelope.To, participant)) {
			filtered = append(filtered, m)
		}
	}
	msgs = filtered

	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}

	var result []RelatedMsg
	for _, m := range msgs {
		if m.Envelope == nil {
			continue
		}
		result = append(result, RelatedMsg{
			UID:     uint32(m.UID),
			Date:    m.Envelope.Date.Format(time.RFC3339),
			From:    fmtImapAddrs(m.Envelope.From),
			To:      fmtImapAddrs(m.Envelope.To),
			Subject: decodeHeader(m.Envelope.Subject),
		})
	}
	return result, nil
}

type RelatedMsg struct {
	UID     uint32
	Date    string
	From    string
	To      string
	Subject string
}

// MailDigestEmail holds parsed email data for the mail digest.
type MailDigestEmail struct {
	UID      uint32
	Date     string
	From     string
	FromAddr string
	To       string
	Subject  string
	Body     string
}

// SenderGroup groups unread emails from a single sender with conversation history.
type SenderGroup struct {
	SenderAddr string
	SenderName string
	Emails     []MailDigestEmail
	History    []RelatedMsg
	Digest     string // filled by caller after sub-agent processing
}

// MailDigestConfig configures FetchUnreadGrouped.
type MailDigestConfig struct {
	SentMailbox  string  // default "Sent"
	SinceHours   float64 // default 24
	ContextHours float64 // default 336 (14 days)
	ProgressFn   func(string)
}

// FetchUnreadGrouped fetches unread emails from INBOX, groups them by sender,
// and retrieves conversation history for each group.
func FetchUnreadGrouped(cfg MailDigestConfig) ([]SenderGroup, error) {
	if cfg.SentMailbox == "" {
		cfg.SentMailbox = "Sent"
	}
	if cfg.SinceHours <= 0 {
		cfg.SinceHours = 24
	}
	if cfg.ContextHours <= 0 {
		cfg.ContextHours = 336
	}
	progress := cfg.ProgressFn
	if progress == nil {
		progress = func(string) {}
	}

	c, err := dialIMAP()
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("SELECT INBOX failed: %w", err)
	}

	// SEARCH UNSEEN + SINCE
	cutoff := time.Now().Add(-time.Duration(cfg.SinceHours * float64(time.Hour)))
	searchDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, cutoff.Location())
	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
		Since:   searchDay,
	}

	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("SEARCH failed: %w", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}

	// Fetch envelopes
	var uidSet imap.UIDSet
	uidSet.AddNum(uids...)
	fetchOpts := &imap.FetchOptions{Envelope: true, UID: true}
	msgs, err := c.Fetch(uidSet, fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("FETCH envelopes failed: %w", err)
	}

	// Client-side time filter (IMAP SINCE is day-level)
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.Envelope != nil && !m.Envelope.Date.Before(cutoff) {
			filtered = append(filtered, m)
		}
	}
	msgs = filtered

	if len(msgs) == 0 {
		return nil, nil
	}

	progress(fmt.Sprintf("Найдено %d непрочитанных писем", len(msgs)))

	// Group by sender email address
	groupMap := map[string]*SenderGroup{}
	var groupOrder []string
	unreadUIDs := map[uint32]bool{}

	for _, m := range msgs {
		unreadUIDs[uint32(m.UID)] = true
		if m.Envelope == nil || len(m.Envelope.From) == 0 {
			continue
		}
		from := m.Envelope.From[0]
		addr := strings.ToLower(fmt.Sprintf("%s@%s", from.Mailbox, from.Host))
		name := decodeHeader(from.Name)

		g, ok := groupMap[addr]
		if !ok {
			g = &SenderGroup{SenderAddr: addr, SenderName: name}
			groupMap[addr] = g
			groupOrder = append(groupOrder, addr)
		}
		g.Emails = append(g.Emails, MailDigestEmail{
			UID:      uint32(m.UID),
			Date:     m.Envelope.Date.Format(time.RFC3339),
			From:     fmtImapAddrs(m.Envelope.From),
			FromAddr: addr,
			To:       fmtImapAddrs(m.Envelope.To),
			Subject:  decodeHeader(m.Envelope.Subject),
		})
	}

	c.Close() // done with envelope connection

	progress(fmt.Sprintf("Сгруппировано в %d отправителей", len(groupOrder)))

	// Per group: fetch email content + search history
	var groups []SenderGroup
	for _, addr := range groupOrder {
		g := groupMap[addr]

		// Cap emails per group
		emails := g.Emails
		if len(emails) > 10 {
			emails = emails[len(emails)-10:]
		}

		progress(fmt.Sprintf("  %s (%d писем)...", g.SenderName, len(emails)))

		// Fetch full content for each email
		for i, e := range emails {
			content, err := fetchEmailContent("INBOX", e.UID)
			if err != nil {
				continue
			}
			emails[i].Body = content.Body
			if emails[i].From == "" {
				emails[i].From = content.From
			}
			if emails[i].To == "" {
				emails[i].To = content.To
			}
		}
		g.Emails = emails

		// Search conversation history in INBOX + Sent
		inboxMsgs, _ := searchRelatedMessages("INBOX", addr, cfg.ContextHours, 15)
		sentMsgs, _ := searchRelatedMessages(cfg.SentMailbox, addr, cfg.ContextHours, 15)

		// Dedup: exclude unread UIDs from inbox history
		for _, r := range inboxMsgs {
			if !unreadUIDs[r.UID] {
				g.History = append(g.History, r)
			}
		}
		g.History = append(g.History, sentMsgs...)

		groups = append(groups, *g)
	}

	return groups, nil
}

func execDigestMessage(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Mailbox      string  `json:"mailbox"`
		UID          uint32  `json:"uid"`
		ContextHours float64 `json:"context_hours"`
		SentMailbox  string  `json:"sent_mailbox"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Mailbox == "" {
		args.Mailbox = "INBOX"
	}
	if args.UID == 0 {
		return "", fmt.Errorf("uid is required")
	}
	if args.ContextHours <= 0 {
		args.ContextHours = 336 // 14 days
	}
	if args.SentMailbox == "" {
		args.SentMailbox = "Sent"
	}
	if SubAgentFn == nil {
		return "", fmt.Errorf("sub-agent not available")
	}

	// 1. Fetch the target email
	email, err := fetchEmailContent(args.Mailbox, args.UID)
	if err != nil {
		return "", err
	}

	// 2. Search conversation history
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== EMAIL ===\nFrom: %s\nTo: %s\nSubject: %s\nDate: %s\n\n%s\n",
		email.From, email.To, email.Subject, email.Date, email.Body))

	hasHistory := false
	if email.FromAddr != "" {
		inboxMsgs, _ := searchRelatedMessages(args.Mailbox, email.FromAddr, args.ContextHours, 15)
		sentMsgs, _ := searchRelatedMessages(args.SentMailbox, email.FromAddr, args.ContextHours, 15)

		sb.WriteString("\n=== CONVERSATION HISTORY ===\n")
		for _, r := range inboxMsgs {
			if r.UID == args.UID {
				continue // skip the target email itself
			}
			hasHistory = true
			sb.WriteString(fmt.Sprintf("[INBOX] %s | From: %s | Subject: %s\n", r.Date, r.From, r.Subject))
		}
		for _, r := range sentMsgs {
			hasHistory = true
			sb.WriteString(fmt.Sprintf("[Sent] %s | To: %s | Subject: %s\n", r.Date, r.To, r.Subject))
		}
	}
	if !hasHistory {
		sb.WriteString("\nNo conversation history found with this sender.\n")
	}

	content := sb.String()
	if len(content) > 60000 {
		content = content[:60000] + "\n[...truncated]"
	}

	// 3. Sub-agent analysis
	systemPrompt := ImapDigestPrompt
	if systemPrompt == "" {
		systemPrompt = `Analyze the email and its conversation history. Provide a structured response:

1. SUMMARY: 2-3 sentence summary of the email
2. CATEGORY: exactly one of: important | needs-reply | invoice/accounting | regular | newsletter/promo
3. CONVERSATION: if history exists, briefly describe the ongoing conversation topic and context. If no history, write "No prior conversation."

Respond in the same language as the email content.`
	}

	summary, err := SubAgentFn(systemPrompt, content)
	if err != nil {
		return "", fmt.Errorf("digest failed: %w", err)
	}

	return fmt.Sprintf("From: %s\nSubject: %s\nDate: %s\n\n%s",
		email.From, email.Subject, email.Date, summary), nil
}

// fmtMailAddrs formats parsed net/mail addresses without RFC 2047 re-encoding.
// (net/mail.Address.String() re-encodes non-ASCII names, which we don't want.)
func fmtMailAddrs(addrs []*netmail.Address) string {
	strs := make([]string, len(addrs))
	for i, a := range addrs {
		if a.Name != "" {
			strs[i] = fmt.Sprintf("%s <%s>", a.Name, a.Address)
		} else {
			strs[i] = a.Address
		}
	}
	return strings.Join(strs, ", ")
}

// decodeHeader decodes RFC 2047 encoded-words (=?charset?encoding?text?=) in a header value.
var mimeWordDecoder = &mime.WordDecoder{}

func decodeHeader(s string) string {
	decoded, err := mimeWordDecoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}

func fmtImapAddrs(addrs []imap.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		name := decodeHeader(a.Name)
		email := fmt.Sprintf("%s@%s", a.Mailbox, a.Host)
		if name != "" {
			parts[i] = fmt.Sprintf("%s <%s>", name, email)
		} else {
			parts[i] = email
		}
	}
	return strings.Join(parts, ", ")
}

// addrMatchesFilter checks if any address in the list matches the filter
// (case-insensitive substring match on decoded name or email).
func addrMatchesFilter(addrs []imap.Address, filter string) bool {
	filter = strings.ToLower(filter)
	for _, a := range addrs {
		name := strings.ToLower(decodeHeader(a.Name))
		email := strings.ToLower(fmt.Sprintf("%s@%s", a.Mailbox, a.Host))
		if strings.Contains(name, filter) || strings.Contains(email, filter) {
			return true
		}
	}
	return false
}
