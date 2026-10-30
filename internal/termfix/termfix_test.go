package termfix

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The value set in init is the one lipgloss reports, so no query is needed.
func TestBackgroundIsSetWithoutAQuery(t *testing.T) {
	if !lipgloss.HasDarkBackground() {
		t.Fatal("lipgloss should report the value set by termfix")
	}
}
