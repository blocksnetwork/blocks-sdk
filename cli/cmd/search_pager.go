package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/pubnub/blocks-sdk/cli/internal/agentsearch"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"golang.org/x/term"
)

const (
	// linesPerEntry is an entry's worst case: name, display name, summary, blank separator.
	linesPerEntry     = 4
	pagerFooterLines  = 4
	pagerMinPageSize  = 3
	pagerDefaultLines = 24

	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	eraseDown  = "\r\x1b[J"
)

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

type pageFetcher func(ctx context.Context, cursor string) (agentsearch.Result, error)

// searchPager caches every page it has shown, so going back never refetches.
type searchPager struct {
	pages  []agentsearch.Result
	index  int
	fetch  pageFetcher
	status string
	// exhausted records that the page after the last cached one came back empty.
	exhausted bool
	showHelp  bool
}

func newSearchPager(first agentsearch.Result, fetch pageFetcher) *searchPager {
	return &searchPager{pages: []agentsearch.Result{first}, fetch: fetch}
}

func (p *searchPager) current() agentsearch.Result { return p.pages[p.index] }

func (p *searchPager) onLastCached() bool { return p.index == len(p.pages)-1 }

func (p *searchPager) canPrev() bool { return p.index > 0 }

func (p *searchPager) canNext() bool {
	return !p.onLastCached() || (!p.exhausted && p.current().Next != "")
}

func (p *searchPager) needsFetch() bool { return p.onLastCached() && p.canNext() }

func (p *searchPager) next(ctx context.Context) {
	p.status = ""
	p.showHelp = false
	if !p.canNext() {
		p.status = "No more results."
		return
	}
	if !p.onLastCached() {
		p.index++
		return
	}
	page, err := p.fetch(ctx, p.current().Next)
	switch {
	case err != nil:
		p.status = err.Error()
	case len(page.Agents) == 0:
		p.exhausted = true
		p.status = "No more results."
	default:
		p.pages = append(p.pages, page)
		p.index++
	}
}

func (p *searchPager) prev() {
	p.status = ""
	p.showHelp = false
	if p.canPrev() {
		p.index--
	}
}

// firstRank is the 1-based position of the current page's first agent.
func (p *searchPager) firstRank() int {
	n := 1
	for _, page := range p.pages[:p.index] {
		n += len(page.Agents)
	}
	return n
}

func terminalRows() int {
	if _, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 0 {
		return h
	}
	return pagerDefaultLines
}

func pagerPageSize() int {
	return min(max((terminalRows()-pagerFooterLines)/linesPerEntry, pagerMinPageSize), agentsearch.MaxLimit)
}

// screenRows is how many terminal rows text occupies at width, counting lines the terminal wraps.
func screenRows(text string, width int) int {
	width = max(width, 1)
	rows := 0
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		cells := runewidth.StringWidth(ansiSequence.ReplaceAllString(line, ""))
		rows += max(1, (cells+width-1)/width)
	}
	return rows
}

// inlineBlock redraws a block of output in place, leaving everything printed above it alone.
type inlineBlock struct{ rows int }

func (b *inlineBlock) draw(text string, width int) {
	var out strings.Builder
	// A block taller than the window has scrolled partly out of reach, so the new one goes below it.
	if b.rows > 0 && b.rows < terminalRows() {
		fmt.Fprintf(&out, "\x1b[%dA", b.rows)
	}
	out.WriteString(eraseDown + strings.ReplaceAll(text, "\n", "\r\n"))
	fmt.Fprint(os.Stdout, out.String())
	b.rows = screenRows(text, width)
}

func (p *searchPager) footer(st searchStyle) string {
	key := func(label string, enabled bool) string {
		if enabled {
			return label
		}
		return st.paint("2", label)
	}
	help := "? syntax"
	if p.showHelp {
		help = "? results"
	}
	first := p.firstRank()
	line := fmt.Sprintf("Page %d · agents %d–%d    %s   %s   %s   q quit",
		p.index+1, first, first+len(p.current().Agents)-1,
		key("← prev", p.canPrev()), key("→ next", p.canNext()), help)
	if p.status != "" {
		line += "\n" + p.status
	}
	return line
}

// runSearchPager shows one page at a time in place, below the command, and leaves the last page viewed.
func runSearchPager(ctx context.Context, query string, first agentsearch.Result, fetch pageFetcher) error {
	keys, stop, ok := wizard.NavKeys()
	if !ok {
		writeSearchResults(os.Stdout, stdoutSearchStyle(), query, first)
		writeSearchFooter(os.Stdout, first, "")
		return nil
	}
	p := newSearchPager(first, fetch)
	var block inlineBlock
	draw := func(withFooter bool) {
		st := stdoutSearchStyle()
		var buf bytes.Buffer
		if p.showHelp {
			writeSearchSyntax(&buf, st)
		} else {
			writeSearchResults(&buf, st, query, p.current())
		}
		if withFooter {
			fmt.Fprintf(&buf, "\n%s\n", p.footer(st))
		}
		block.draw(buf.String(), st.width)
	}

	quit := func() error {
		stop()
		p.showHelp = false
		draw(false)
		fmt.Fprint(os.Stdout, showCursor)
		writeSearchFooter(os.Stdout, p.current(), "")
		return nil
	}

	fmt.Fprint(os.Stdout, hideCursor)
	draw(true)
	for k := range keys {
		switch k {
		case wizard.NavNext:
			if !p.needsFetch() {
				p.next(ctx)
				break
			}
			p.status = "Loading…"
			draw(true)
			if !loadNext(ctx, p, keys) {
				return quit()
			}
		case wizard.NavPrev:
			p.prev()
		case wizard.NavHelp:
			p.showHelp = !p.showHelp
		case wizard.NavQuit:
			return quit()
		}
		draw(true)
	}
	fmt.Fprint(os.Stdout, showCursor)
	return nil
}

// loadNext fetches off the key loop so a quit key cancels a stalled request; false means the user quit.
func loadNext(ctx context.Context, p *searchPager, keys <-chan wizard.NavKey) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.next(ctx)
		close(done)
	}()
	for {
		select {
		case <-done:
			return true
		case k := <-keys:
			if k == wizard.NavQuit {
				cancel()
				<-done
				return false
			}
		}
	}
}
