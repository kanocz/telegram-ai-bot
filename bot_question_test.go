package main

import (
	"testing"
	"time"
)

func TestAnswerTextQuestionIgnoresEmptyText(t *testing.T) {
	const chatID = 42
	pq := newPendingQuestion(chatID, nil)
	registerTextQuestion(chatID, pq)
	t.Cleanup(func() { pendingTextQuestions.Delete(chatID) })

	// A photo reply arrives: no text to deliver. Taking the question out here
	// used to drop it and leave the asking goroutine blocked forever.
	if answerTextQuestion(chatID, "") {
		t.Fatal("empty text was treated as an answer")
	}

	// The question must still be there for the next, real reply.
	if !answerTextQuestion(chatID, "сыр") {
		t.Fatal("question was lost after the empty reply")
	}

	select {
	case got := <-pq.ResultCh:
		if got != "сыр" {
			t.Errorf("got %q, want %q", got, "сыр")
		}
	default:
		t.Error("no answer delivered")
	}
}

func TestAnswerTextQuestionWithNoPendingQuestion(t *testing.T) {
	if answerTextQuestion(999999, "hello") {
		t.Error("reported an answer for a chat with no pending question")
	}
}

func TestRegisterTextQuestionReleasesPrevious(t *testing.T) {
	const chatID = 43
	first := newPendingQuestion(chatID, nil)
	registerTextQuestion(chatID, first)

	done := make(chan error, 1)
	go func() {
		_, err := first.await(func() {})
		done <- err
	}()

	// A second question for the same chat replaces the first. Only one can be
	// answered, so the first waiter has to be let go rather than left hanging.
	second := newPendingQuestion(chatID, nil)
	registerTextQuestion(chatID, second)
	t.Cleanup(func() { pendingTextQuestions.Delete(chatID) })

	select {
	case err := <-done:
		if err == nil {
			t.Error("superseded waiter returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("superseded waiter is still blocked")
	}

	// The replacement still works.
	if !answerTextQuestion(chatID, "да") {
		t.Fatal("replacement question did not accept an answer")
	}
}

func TestAwaitReturnsAnswer(t *testing.T) {
	pq := newPendingQuestion(1, nil)
	pq.ResultCh <- "готово"

	got, err := pq.await(func() { t.Error("unregister called on a normal answer") })
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if got != "готово" {
		t.Errorf("got %q, want %q", got, "готово")
	}
}
