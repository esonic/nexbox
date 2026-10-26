package agent

import (
	"errors"
	"testing"
)

// TestSessionManager_RemoveInterjectorScopedByGeneration guards the
// turn-handoff race: the old turn's deferred removal runs after
// processNextQueued may have started the next turn, so it must only remove
// its own registration — never the successor's.
func TestSessionManager_RemoveInterjectorScopedByGeneration(t *testing.T) {
	sm := NewSessionManager()
	oldGen := sm.SetInterjector("s1", func(string) error { return errors.New("old") })
	newGen := sm.SetInterjector("s1", func(string) error { return nil })

	// Old turn cleans up late — the new turn's injector must survive.
	sm.RemoveInterjector("s1", oldGen)
	ok, err := sm.Interject("s1", "hello")
	if !ok || err != nil {
		t.Fatalf("new-generation injector was removed by an old-generation cleanup: ok=%v err=%v", ok, err)
	}

	// The owning generation can remove it.
	sm.RemoveInterjector("s1", newGen)
	if ok, _ := sm.Interject("s1", "hello"); ok {
		t.Fatal("injector still registered after owner removed it")
	}
}

// TestSessionManager_RemoveInterjectorZeroGenIsNoop: a turn that never
// registered (gen 0) must not remove anyone's injector.
func TestSessionManager_RemoveInterjectorZeroGenIsNoop(t *testing.T) {
	sm := NewSessionManager()
	sm.SetInterjector("s1", func(string) error { return nil })
	sm.RemoveInterjector("s1", 0)
	if ok, _ := sm.Interject("s1", "hello"); !ok {
		t.Fatal("gen-0 removal deleted a live injector")
	}
}

// TestSessionManager_LatestRegistrationWins: re-registering replaces the
// injector, and Interject always uses the latest.
func TestSessionManager_LatestRegistrationWins(t *testing.T) {
	sm := NewSessionManager()
	var called string
	sm.SetInterjector("s1", func(string) error { called = "first"; return nil })
	sm.SetInterjector("s1", func(string) error { called = "second"; return nil })
	if ok, err := sm.Interject("s1", "x"); !ok || err != nil {
		t.Fatalf("Interject: ok=%v err=%v", ok, err)
	}
	if called != "second" {
		t.Fatalf("Interject used %q, want the latest registration", called)
	}
}
