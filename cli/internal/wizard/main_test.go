package wizard

import (
	"os"
	"testing"

	"github.com/mattn/go-runewidth"
)

// Ambiguous-width glyphs (—, ›, ↑↓) measure two cells under a CJK locale or
// RUNEWIDTH_EASTASIAN=1, either of which would break the cell expectations.
func TestMain(m *testing.M) {
	saved := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = false
	code := m.Run()
	runewidth.DefaultCondition.EastAsianWidth = saved
	os.Exit(code)
}
