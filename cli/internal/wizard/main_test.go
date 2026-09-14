package wizard

import (
	"os"
	"testing"

	"github.com/mattn/go-runewidth"
)

// TestMain pins the terminal-cell width condition for the whole package.
// go-runewidth derives EastAsianWidth from the ambient locale
// (RUNEWIDTH_EASTASIAN, LC_ALL/LC_CTYPE/LANG with a ja_*/ko_*/zh_* prefix),
// and under that condition the East Asian Ambiguous glyphs the renderers
// use (—, ›, ↑, ↓) measure two cells instead of one. The renderer behaves
// consistently either way — it and the terminal agree — but the tests'
// hard-coded cell expectations assume Ambiguous = one cell, so a CJK-locale
// machine would otherwise see locale-dependent failures. Pinning here makes
// the expectations true everywhere.
func TestMain(m *testing.M) {
	saved := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = false
	code := m.Run()
	runewidth.DefaultCondition.EastAsianWidth = saved
	os.Exit(code)
}
