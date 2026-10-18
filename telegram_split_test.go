package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTelegramMessageKeepsChunksValid(t *testing.T) {
	cases := map[string]string{
		// Cyrillic without a newline anywhere: the old byte cut landed in the
		// middle of a two-byte rune and Telegram rejected the chunk.
		"cyrillic no breaks": strings.Repeat("я", 5000),
		"cyrillic words":     strings.Repeat("привет ", 2000),
		"paragraphs":         strings.Repeat("Строка текста.\n", 800),
		"mixed":              strings.Repeat("ascii текст 123 ", 1000),
		"long ascii run":     strings.Repeat("x", 9000),
	}

	for name, text := range cases {
		chunks := splitTelegramMessage(text)
		if len(chunks) == 0 {
			t.Errorf("%s: no chunks", name)
			continue
		}
		for i, c := range chunks {
			if !utf8.ValidString(c) {
				t.Errorf("%s: chunk %d is not valid UTF-8", name, i)
			}
			if len(c) > telegramSplitBudget {
				t.Errorf("%s: chunk %d is %d bytes, over the %d budget", name, i, len(c), telegramSplitBudget)
			}
			if c == "" {
				t.Errorf("%s: chunk %d is empty", name, i)
			}
		}
		if got := strings.Join(chunks, ""); got != text {
			t.Errorf("%s: rejoined chunks do not match the input (%d vs %d bytes)", name, len(got), len(text))
		}
	}
}

func TestSplitTelegramMessageShortTextStaysWhole(t *testing.T) {
	text := "короткое сообщение"
	chunks := splitTelegramMessage(text)
	if len(chunks) != 1 || chunks[0] != text {
		t.Errorf("got %q, want one chunk equal to the input", chunks)
	}
}

func TestMarkdownChunkConversionStaysUnderLimit(t *testing.T) {
	// Each chunk is converted on its own, so conversion must not push it past
	// Telegram's limit for ordinary text.
	text := strings.Repeat("**жирный** текст со `кодом` и ссылками. ", 500)
	for i, chunk := range splitTelegramMessage(text) {
		if html := markdownToTelegramHTML(chunk); len(html) > telegramMaxLen {
			t.Errorf("chunk %d converts to %d bytes, over the %d limit", i, len(html), telegramMaxLen)
		}
	}
}
