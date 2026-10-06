package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/agentsearch"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

func page(next string, names ...string) agentsearch.Result {
	r := agentsearch.Result{Next: next}
	for _, n := range names {
		r.Agents = append(r.Agents, agentsearch.Agent{AgentName: n})
	}
	return r
}

// fakeFetch serves pages by cursor and counts calls, so a test can tell a cached page from a refetch.
func fakeFetch(pages map[string]agentsearch.Result, calls *int) pageFetcher {
	return func(_ context.Context, cursor string) (agentsearch.Result, error) {
		*calls++
		if r, ok := pages[cursor]; ok {
			return r, nil
		}
		return agentsearch.Result{}, errors.New("search failed: cannot reach example.com")
	}
}

func TestSearchPagerGoesForwardAndBackWithoutRefetching(t *testing.T) {
	calls := 0
	p := newSearchPager(page("c2", "a", "b"), fakeFetch(map[string]agentsearch.Result{
		"c2": page("", "c"),
	}, &calls))

	p.next(context.Background())
	if got := p.current().Agents[0].AgentName; got != "c" || p.firstRank() != 3 {
		t.Fatalf("after next: first agent %q at rank %d, want c at 3", got, p.firstRank())
	}
	if p.canNext() {
		t.Error("canNext on a page with no cursor")
	}
	p.prev()
	if got := p.current().Agents[0].AgentName; got != "a" || p.firstRank() != 1 || p.canPrev() {
		t.Fatalf("after prev: first agent %q at rank %d, canPrev %v; want a at 1 with no prev", got, p.firstRank(), p.canPrev())
	}
	p.next(context.Background())
	if got := p.current().Agents[0].AgentName; got != "c" || p.status != "" || calls != 1 {
		t.Errorf("after next again: first agent %q, status %q, %d fetches; want c from the cache after 1 fetch", got, p.status, calls)
	}
}

func TestSearchPagerStopsAtAnEmptyPageAfterAFullOne(t *testing.T) {
	calls := 0
	p := newSearchPager(page("c2", "a", "b"), fakeFetch(map[string]agentsearch.Result{
		"c2": page(""),
	}, &calls))

	p.next(context.Background())
	if p.index != 0 || p.status != "No more results." || p.canNext() {
		t.Errorf("index %d, status %q, canNext %v; want to stay on page 1 with no next", p.index, p.status, p.canNext())
	}
}

func TestSearchPagerKeepsThePageWhenAFetchFails(t *testing.T) {
	calls := 0
	p := newSearchPager(page("broken", "a"), fakeFetch(nil, &calls))

	p.next(context.Background())
	if p.index != 0 || p.status != "search failed: cannot reach example.com" || !p.canNext() {
		t.Errorf("index %d, status %q, canNext %v; want page 1 kept, the error shown, and a retry allowed", p.index, p.status, p.canNext())
	}
}

func TestLoadNextQuitCancelsAStalledFetch(t *testing.T) {
	canceled := make(chan struct{})
	stalled := func(ctx context.Context, _ string) (agentsearch.Result, error) {
		<-ctx.Done()
		close(canceled)
		return agentsearch.Result{}, ctx.Err()
	}
	p := newSearchPager(page("c2", "a"), stalled)
	keys := make(chan wizard.NavKey, 1)
	keys <- wizard.NavQuit

	if loadNext(context.Background(), p, keys) {
		t.Fatal("loadNext = true, want false: a quit while loading must end the pager")
	}
	select {
	case <-canceled:
	default:
		t.Error("the stalled fetch was not canceled")
	}
}

// The redraw moves the cursor up by this count, so a miscount smears pages or eats the lines above.
func TestScreenRowsCountsWrappedLinesAndIgnoresColor(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  int
	}{
		{"one line per newline", "a\nb\n\nc\n", 80, 4},
		{"a line wider than the terminal wraps", strings.Repeat("x", 25) + "\n", 10, 3},
		{"color codes take no cells", "\x1b[1m" + strings.Repeat("x", 10) + "\x1b[0m\n", 10, 1},
		{"wide characters take two cells", strings.Repeat("文", 6) + "\n", 10, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := screenRows(tc.text, tc.width); got != tc.want {
				t.Errorf("screenRows = %d, want %d", got, tc.want)
			}
		})
	}
}
