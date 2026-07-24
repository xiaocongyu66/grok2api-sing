// Package toolslimit enforces the absolute tools-array ceiling for request payloads.
//
// xAI / Grok Build hard-rejects more than HardMax tools. There is no dynamic
// soft limit: the effective ceiling is always HardMax (aligned with upstream
// chenyme/grok2api, which does not lower the limit based on recent traffic).
package toolslimit

import (
	"fmt"
)

const (
	// HardMax is the absolute upstream ceiling (xAI / Grok Build).
	HardMax = 250
)

// Current returns the effective tools limit (always HardMax).
func Current() int {
	return HardMax
}

// Observe is retained for call-site compatibility; counts are not sampled.
func Observe(count int) {}

// Check returns an error when count exceeds HardMax.
func Check(count int) error {
	if count <= 0 {
		return nil
	}
	if count > HardMax {
		return fmt.Errorf("tools 数量超过上限：提供了 %d 个，上限 %d", count, HardMax)
	}
	return nil
}
