package toolslimit

import (
	"strings"
	"testing"
)

func TestCheckHardMax(t *testing.T) {
	if Current() != HardMax {
		t.Fatalf("Current()=%d want %d", Current(), HardMax)
	}
	if err := Check(100); err != nil {
		t.Fatalf("under hard max: %v", err)
	}
	if err := Check(HardMax); err != nil {
		t.Fatalf("at hard max: %v", err)
	}
	if err := Check(HardMax + 1); err == nil {
		t.Fatal("expected rejection above hard max")
	} else if !strings.Contains(err.Error(), "250") {
		t.Fatalf("error should mention hard max, got %v", err)
	}
	// Observe is a no-op and must not change the limit.
	Observe(1)
	if Current() != HardMax {
		t.Fatalf("after Observe Current()=%d want %d", Current(), HardMax)
	}
	if err := Check(74); err != nil {
		t.Fatalf("74 tools must pass fixed HardMax: %v", err)
	}
}
