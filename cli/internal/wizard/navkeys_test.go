package wizard

import "testing"

func TestNavKeyForPagesOnlyWithLeftAndRight(t *testing.T) {
	cases := []struct {
		name string
		ev   acEvent
		want NavKey
	}{
		{"right arrow pages forward", acEvent{kind: acRight}, NavNext},
		{"left arrow pages back", acEvent{kind: acLeft}, NavPrev},
		// Terminals send Up/Down for the scroll wheel; paging on them turned scrolling into page flips.
		{"up arrow is ignored", acEvent{kind: acUp}, NavNone},
		{"down arrow is ignored", acEvent{kind: acDown}, NavNone},
		{"q quits", acEvent{kind: acRune, r: 'q'}, NavQuit},
		{"Q quits", acEvent{kind: acRune, r: 'Q'}, NavQuit},
		{"esc quits", acEvent{kind: acEsc}, NavQuit},
		{"ctrl+c quits", acEvent{kind: acCtrlC}, NavQuit},
		{"closed input quits", acEvent{kind: acEOF}, NavQuit},
		{"? asks for help", acEvent{kind: acRune, r: '?'}, NavHelp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := navKeyFor(tc.ev); got != tc.want {
				t.Errorf("navKeyFor = %v, want %v", got, tc.want)
			}
		})
	}
}
