package executor

import (
	"os"
	"testing"
)

// TestMain lets the test binary stand in for the firstlume binary when
// Terragrunt runs it as the engine (see tfwrap.go).
func TestMain(m *testing.M) {
	if code, ok := EngineWrapper(); ok {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
