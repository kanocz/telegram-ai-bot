package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewsListButtonsAndRefs(t *testing.T) {
	newsListDir = t.TempDir()
	l := newsList{ID: "abc1", Header: "H"}
	for i := 0; i < 10; i++ {
		l.Topics = append(l.Topics, topicGroup{TopicTitle: "t"})
	}
	kb := newsListKeyboard(l)
	if len(kb.InlineKeyboard) != 2 || len(kb.InlineKeyboard[1]) != 5 {
		t.Fatalf("want 2 rows of 5, got %v", kb.InlineKeyboard)
	}
	if d := kb.InlineKeyboard[1][4].CallbackData; d != "nd:abc1:10" || len(d) > 64 {
		t.Fatalf("callback data %q", d)
	}

	// Stale file is pruned on the next save; the fresh one stays.
	stale := filepath.Join(newsListDir, "old.json")
	os.WriteFile(stale, []byte("{}"), 0o644)
	old := time.Now().Add(-newsListTTL - time.Hour)
	os.Chtimes(stale, old, old)
	if err := saveNewsList(l); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale list not pruned")
	}

	for _, bad := range []string{"nd:../x:1", "nd:ABC:1", "nd::1", "nd:abc1:x", "nd:abc1:0", "nd:abc1:11"} {
		if _, err := runNewsDive(modelConfig{}, "", bad, nil, nil, nil, nil, thinkDefault, nil); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
	msg, err := runNewsDive(modelConfig{}, "", "nd:gone:1", nil, nil, nil, nil, thinkDefault, nil)
	if err != nil || !strings.Contains(msg, "устарел") {
		t.Fatalf("expired list: %q %v", msg, err)
	}
}
