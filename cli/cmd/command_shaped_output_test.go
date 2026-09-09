package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This defect has now appeared four times — the two pin declines, the init next-steps
// block, the logout summary and the deploy divergence warning, and the profile-removal
// retry — and each round was found by reading, then pinned by a case that covers exactly
// the site that was found. That is the wrong shape of test for a defect that keeps
// arriving somewhere new: every one of those cases passes on a fifth site nobody has
// written yet.
//
// So this one takes the package's own source as its input, the way
// TestEveryBlocksEnvironmentVariableIsClassified takes the tree's BLOCKS_* names. The
// rule is the one the four rounds all landed on: a *line* that reads as something to run
// carries no interpolated value. Naming the command is fine, naming a placeholder is
// fine, and the value goes on a line of its own where the user reads it and supplies it
// themselves.
//
// What it cannot know is whether a particular interpolated value is untrustworthy — that
// needs the type of the argument, and the argument is usually a local variable. It does
// not need to: the allowlist below is short, every entry on it is a compile-time
// constant, and a new site has to justify itself in the same one line. An interpolation
// that turns out to be a constant costs whoever adds it a line here; one that turns out
// to be a profile name, a URL from a .env or a name from the registry costs a user their
// shell.
//
// Its other blind spot is worth naming, because it is why the output scans stay: a
// literal whose runnable words are themselves interpolated is invisible here.
// withheldEnvFileNote is exactly that — the word "export" arrives through a %s — so this
// scan says nothing about it, and TestAWithheldVariableCanOnlyBeOneOfAFixedSetOfNames
// pins the property that makes it safe instead.

// runnableSegmentsOf splits a string literal into the segments a reader sees as separate
// lines, and returns those that read as something to run. The split is by newline
// because that is the unit commandLines works in: a message whose value is on one line
// and whose command is on the next is exactly the shape the fixes produced, and it must
// not be reported as a violation.
func runnableSegmentsOf(literal string) []string {
	var out []string
	for _, segment := range strings.Split(literal, "\n") {
		for _, marker := range runnableMarkers {
			if strings.Contains(segment, marker) {
				out = append(out, segment)
				break
			}
		}
	}
	return out
}

// formatVerb matches a printf verb, so a segment carrying one is a segment that
// interpolates a value. `%%` is a literal percent and is deliberately not a verb.
var formatVerb = regexp.MustCompile(`%[-+ #0-9.*\[\]]*[a-zA-Z]`)

// runnableSegmentsThatMayInterpolate are the segments that read as something to run and
// interpolate a value anyway, each with the reason that is safe. Every one of them
// interpolates a compile-time constant — a variable name this package spells itself, or
// an adapter/command name from a fixed set — so no attacker-supplied byte can reach the
// line. Matching is on the segment text, so rewording a message brings its entry back
// here for a fresh decision, which is the point.
var runnableSegmentsThatMayInterpolate = map[string]string{
	"  To use it, log in to that deployment with 'blocks login', or export %s in your shell.":                    "the variable name, a constant in this package",
	"  To use it, pin the deployment that does (%s in %s), or export %s in your shell.":                          "two variable names, both constants, and the project file path this process chose",
	", or run 'blocks login --provider %s' once to store a token":                                                "the hosting adapter's own name, from the fixed set of adapters",
	"authentication failed — run 'blocks login' to re-authenticate, then retry '%s'":                             "the command's own name, a constant in this package",
	"blocks.config.json not found or invalid — run 'blocks init <name> --mode webapp --agent <agent>' first: %w": "the wrapped parse error, which no remedy is built from",
}

// TestNoCommandShapedOutputInterpolatesAValue scans every non-test source file in this
// package. It is deliberately a source scan and not an output scan: the outputs that
// matter are produced deep inside command bodies that need a deployment, a store and a
// project directory to reach, so enumerating them at runtime is exactly what the four
// site-specific cases already do — one at a time, and only for sites someone thought of.
func TestNoCommandShapedOutputInterpolatesAValue(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no source files at all; the scan is not looking at the package")
	}
	fset := token.NewFileSet()
	seen, checked := map[string]bool{}, 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, segment := range runnableSegmentsOf(value) {
				checked++
				if _, allowed := runnableSegmentsThatMayInterpolate[segment]; allowed {
					seen[segment] = true
					continue
				}
				if !formatVerb.MatchString(segment) {
					continue
				}
				t.Errorf("%s: this line reads as something to run and interpolates a value:\n\t%q\nName the command and let the user supply the value — a line presented as something to paste gets pasted, and a profile name, a URL from a project .env or an agent name from the registry can carry `;` or $(...). If the value really is a compile-time constant, add the line to runnableSegmentsThatMayInterpolate with the reason.",
					fset.Position(lit.Pos()), segment)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no line of this package reads as something to run; the scan has stopped looking")
	}
	// And the reverse, so an allowlist entry cannot outlive the line it vouches for and
	// silently cover something else later.
	for segment := range runnableSegmentsThatMayInterpolate {
		if !seen[segment] {
			t.Errorf("this allowed line no longer exists in the package; drop its entry:\n\t%q", segment)
		}
	}
}

// The one line in this package that reads as something to run and names an
// attacker-chosen value: the withheld-variables note tells the user to export the
// variables it did not import, and names them in the spelling their .env used. It is safe
// because of a property of the gate in front of it rather than of the formatter — the
// name reached the note only by matching explainedWithheldKeys under an uppercase fold,
// so it is a case variant of a fixed name and cannot carry a shell metacharacter at all.
//
// That property is what this pins, over the real loader and a real file, because it is
// the whole argument for the interpolation being allowed. If a fold ever admits more than
// a case variant, the note has to lose the name.
func TestAWithheldVariableCanOnlyBeOneOfAFixedSetOfNames(t *testing.T) {
	restoreCLIState(t)
	// A name that would be explained, in a spelling that is not the canonical one, and
	// two that carry a command: the loader must explain the first and say nothing about
	// the others, because neither is a variable this CLI reads.
	const explained = "https_proxy"
	const injected = "HTTPS_PROXY;$(id)"
	const alsoInjected = "https_proxy`id`"
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		explained+"=http://proxy.acme.example.test:3128\n"+
			injected+"=http://proxy.attacker.example.test\n"+
			alsoInjected+"=http://proxy.attacker.example.test\n"))
	loadProjectEnv(t, explained, injected, alsoInjected)
	// Cloned, not aliased: the loader clears these maps in place, so keeping the
	// reference would restore the emptied original.
	priorKeys, priorValues := maps.Clone(envFileKeys), maps.Clone(projectEnvValues)
	t.Cleanup(func() { envFileKeys, projectEnvValues = priorKeys, priorValues })

	note := captureStdoutStderr(func() { loadEnvFile(".env") })

	if !strings.Contains(note, explained) {
		t.Fatalf("premise: a withheld variable worth explaining must be named:\n%s", note)
	}
	for _, name := range []string{injected, alsoInjected} {
		if strings.Contains(note, name) {
			t.Errorf("the note names %q, which is not a variable this CLI reads:\n%s", name, note)
		}
	}
	assertNoInjectedValueInCommandLines(t, note, injected, alsoInjected)
}
