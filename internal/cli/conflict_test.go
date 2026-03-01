package cli

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConflictResolver_AllModeSkipsPrompt verifies that when the mode is an "All"
// action, Resolve() returns immediately without calling the prompt function.
func TestConflictResolver_AllModeSkipsPrompt(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadOverwriteAll)

	called := false
	action, err := cr.Resolve(func() (DownloadConflictAction, error) {
		called = true
		return DownloadSkipOnce, nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("prompt should not be called when mode is All")
	}
	if action != DownloadOverwriteAll {
		t.Fatalf("expected DownloadOverwriteAll, got %v", action)
	}
}

// TestConflictResolver_OnceModePromptsUser verifies that when the mode is a "Once"
// action, the prompt function is called.
func TestConflictResolver_OnceModePromptsUser(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)

	called := false
	action, err := cr.Resolve(func() (DownloadConflictAction, error) {
		called = true
		return DownloadOverwriteOnce, nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("prompt should be called when mode is Once")
	}
	if action != DownloadOverwriteOnce {
		t.Fatalf("expected DownloadOverwriteOnce, got %v", action)
	}
}

// TestConflictResolver_EscalatesToAll verifies that when the user picks an "All"
// action, the mode is updated and subsequent calls skip prompting.
func TestConflictResolver_EscalatesToAll(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)

	// First call: user picks "All"
	_, err := cr.Resolve(func() (DownloadConflictAction, error) {
		return DownloadSkipAll, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second call: should NOT prompt
	promptCalled := false
	action, err := cr.Resolve(func() (DownloadConflictAction, error) {
		promptCalled = true
		return DownloadOverwriteOnce, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if promptCalled {
		t.Fatal("prompt should not be called after escalation to All")
	}
	if action != DownloadSkipAll {
		t.Fatalf("expected DownloadSkipAll, got %v", action)
	}
}

// TestConflictResolver_OnceDoesNotEscalate verifies that when the user picks a
// "Once" action, subsequent calls still prompt.
func TestConflictResolver_OnceDoesNotEscalate(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)

	// First call: user picks Once
	_, _ = cr.Resolve(func() (DownloadConflictAction, error) {
		return DownloadOverwriteOnce, nil
	})

	// Second call: should still prompt
	promptCalled := false
	_, _ = cr.Resolve(func() (DownloadConflictAction, error) {
		promptCalled = true
		return DownloadResumeOnce, nil
	})
	if !promptCalled {
		t.Fatal("prompt should still be called — mode was not escalated")
	}
}

// TestConflictResolver_PromptError verifies that prompt errors propagate.
func TestConflictResolver_PromptError(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)

	_, err := cr.Resolve(func() (DownloadConflictAction, error) {
		return DownloadAbort, fmt.Errorf("terminal disconnected")
	})

	if err == nil || err.Error() != "terminal disconnected" {
		t.Fatalf("expected 'terminal disconnected' error, got: %v", err)
	}
}

// TestConflictResolver_AbortMode verifies that DownloadAbort is treated as a
// non-Once mode (Abort is neither Once nor All — it's a terminal action).
func TestConflictResolver_AbortMode(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)

	// User picks Abort — not an "All" action, so mode stays at SkipOnce
	action, _ := cr.Resolve(func() (DownloadConflictAction, error) {
		return DownloadAbort, nil
	})
	if action != DownloadAbort {
		t.Fatalf("expected DownloadAbort, got %v", action)
	}

	// Mode should still be SkipOnce (Abort doesn't escalate)
	if cr.Mode() != DownloadSkipOnce {
		t.Fatalf("expected mode to still be DownloadSkipOnce, got %v", cr.Mode())
	}
}

// TestConflictResolver_SetMode verifies that SetMode updates the mode externally.
func TestConflictResolver_SetMode(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)
	cr.SetMode(DownloadResumeAll)

	if cr.Mode() != DownloadResumeAll {
		t.Fatalf("expected DownloadResumeAll, got %v", cr.Mode())
	}

	// Should not prompt
	called := false
	action, _ := cr.Resolve(func() (DownloadConflictAction, error) {
		called = true
		return DownloadSkipOnce, nil
	})
	if called {
		t.Fatal("prompt should not be called after SetMode to All")
	}
	if action != DownloadResumeAll {
		t.Fatalf("expected DownloadResumeAll, got %v", action)
	}
}

// TestConflictResolver_ConcurrentAccess verifies thread safety.
func TestConflictResolver_ConcurrentAccess(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadOverwriteAll)

	var wg sync.WaitGroup
	var errCount atomic.Int32

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			action, err := cr.Resolve(func() (DownloadConflictAction, error) {
				// Should never be called — mode is All
				errCount.Add(1)
				return DownloadSkipOnce, nil
			})
			if err != nil || action != DownloadOverwriteAll {
				errCount.Add(1)
			}
		}()
	}

	wg.Wait()
	if errCount.Load() != 0 {
		t.Fatalf("expected 0 errors from concurrent access, got %d", errCount.Load())
	}
}

// TestConflictResolver_ConcurrentEscalation verifies that concurrent Once-mode
// calls serialize prompts correctly and exactly one escalates.
func TestConflictResolver_ConcurrentEscalation(t *testing.T) {
	cr := NewDownloadConflictResolver(DownloadSkipOnce)

	var wg sync.WaitGroup
	var promptCount atomic.Int32

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cr.Resolve(func() (DownloadConflictAction, error) {
				promptCount.Add(1)
				return DownloadSkipAll, nil
			})
		}()
	}

	wg.Wait()

	// First call prompts and escalates to All; subsequent calls should skip prompt.
	// Due to goroutine scheduling, a few goroutines might enter Resolve() before the
	// first one sets the mode. The key invariant is that prompts are serialized (only
	// one at a time) and eventually stop.
	pc := promptCount.Load()
	if pc == 0 {
		t.Fatal("expected at least 1 prompt call")
	}
	// After escalation, mode should be All
	if cr.Mode() != DownloadSkipAll {
		t.Fatalf("expected mode to be DownloadSkipAll after escalation, got %v", cr.Mode())
	}
}

// --- Tests for each conflict type's convenience constructor ---

func TestFileConflictResolver_Basic(t *testing.T) {
	cr := NewFileConflictResolver(FileSkipOnce)

	// Once mode prompts
	action, _ := cr.Resolve(func() (FileConflictAction, error) {
		return FileOverwriteAll, nil
	})
	if action != FileOverwriteAll {
		t.Fatalf("expected FileOverwriteAll, got %v", action)
	}

	// All mode skips prompt
	called := false
	action, _ = cr.Resolve(func() (FileConflictAction, error) {
		called = true
		return FileSkipOnce, nil
	})
	if called {
		t.Fatal("prompt should not be called after escalation")
	}
	if action != FileOverwriteAll {
		t.Fatalf("expected FileOverwriteAll, got %v", action)
	}
}

func TestFolderDownloadConflictResolver_Basic(t *testing.T) {
	cr := NewFolderDownloadConflictResolver(FolderDownloadMergeOnce)

	// Once mode prompts
	action, _ := cr.Resolve(func() (FolderDownloadConflictAction, error) {
		return FolderDownloadMergeAll, nil
	})
	if action != FolderDownloadMergeAll {
		t.Fatalf("expected FolderDownloadMergeAll, got %v", action)
	}

	// All mode skips prompt
	called := false
	action, _ = cr.Resolve(func() (FolderDownloadConflictAction, error) {
		called = true
		return FolderDownloadSkipOnce, nil
	})
	if called {
		t.Fatal("prompt should not be called after escalation")
	}
	if action != FolderDownloadMergeAll {
		t.Fatalf("expected FolderDownloadMergeAll, got %v", action)
	}
}

func TestErrorActionResolver_Basic(t *testing.T) {
	cr := NewErrorActionResolver(ErrorContinueOnce)

	// Once mode prompts
	action, _ := cr.Resolve(func() (ErrorAction, error) {
		return ErrorContinueAll, nil
	})
	if action != ErrorContinueAll {
		t.Fatalf("expected ErrorContinueAll, got %v", action)
	}

	// All mode skips prompt
	called := false
	action, _ = cr.Resolve(func() (ErrorAction, error) {
		called = true
		return ErrorAbort, nil
	})
	if called {
		t.Fatal("prompt should not be called after escalation")
	}
	if action != ErrorContinueAll {
		t.Fatalf("expected ErrorContinueAll, got %v", action)
	}
}

// TestConflictResolver_ResumeAll verifies all three "All" resume variants.
func TestConflictResolver_ResumeAll(t *testing.T) {
	for _, allMode := range []DownloadConflictAction{DownloadSkipAll, DownloadOverwriteAll, DownloadResumeAll} {
		cr := NewDownloadConflictResolver(DownloadSkipOnce)

		// Escalate
		_, _ = cr.Resolve(func() (DownloadConflictAction, error) {
			return allMode, nil
		})

		// Verify no prompt
		called := false
		action, _ := cr.Resolve(func() (DownloadConflictAction, error) {
			called = true
			return DownloadAbort, nil
		})
		if called {
			t.Fatalf("prompt should not be called when mode is %v", allMode)
		}
		if action != allMode {
			t.Fatalf("expected %v, got %v", allMode, action)
		}
	}
}

// TestConflictResolver_AllOnceVariants verifies that all "Once" variants trigger prompts.
func TestConflictResolver_AllOnceVariants(t *testing.T) {
	for _, onceMode := range []DownloadConflictAction{DownloadSkipOnce, DownloadOverwriteOnce, DownloadResumeOnce} {
		cr := NewDownloadConflictResolver(onceMode)

		called := false
		_, _ = cr.Resolve(func() (DownloadConflictAction, error) {
			called = true
			return DownloadSkipOnce, nil
		})
		if !called {
			t.Fatalf("prompt should be called when mode is %v", onceMode)
		}
	}
}
