package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// committedCounts is the counts file as the tests reach it: cmd/llmbench is the working
// directory of every test in this package, and the constants goldset-refit rewrites are
// declared in this very file's neighbour.
const committedCounts = "goldset_test.go"

// TestDerivedCountsMatchTheCommittedConstants is the guard that goldset-refit's
// arithmetic IS the arithmetic of the tests that read these constants. Without it the
// verb could confidently rewrite a correct constant into a wrong one, and the counts
// exist precisely so a Gold Set change cannot pass unseen.
//
// It costs one gold-set read and one gate replay and runs no fit: the two fits this
// package already carries are most of its runtime.
func TestDerivedCountsMatchTheCommittedConstants(t *testing.T) {
	snapshot, err := snapshotRecord("extract-goldset")
	if err != nil {
		t.Fatalf("snapshot the committed record: %v", err)
	}
	_, lits, err := readCountLiterals(committedCounts, derivedCountNames())
	if err != nil {
		t.Fatalf("read the committed counts: %v", err)
	}

	for _, c := range derivedCounts {
		t.Run(c.Name, func(t *testing.T) {
			got, want := c.Value(snapshot), lits[c.Name].Value
			if got != want {
				t.Errorf("the record determines %s = %d but %s records %d (%s). "+
					"Either the record moved and the constant is owed the same commit, or derivedCounts no longer mirrors the assertion that reads it -- fix whichever is wrong, never a label.",
					c.Name, got, committedCounts, want, c.Why)
			}
		})
	}
}

// TestEveryDerivedCountInTheCountsFileIsOwned refuses a count nobody decided about. A
// new integer constant in the counts file is either determined by the record -- and a
// refit must keep it true -- or it is not, and the reason belongs on the record.
//
// It looks at INTEGER literals at file scope only, which is what leaves
// randomStreamAcceptRate (a float: #261's census measurement of the live stream, never
// a derivation from the file) and lonePostingLD (a string fixture) outside the guard
// structurally rather than by an exception list.
func TestEveryDerivedCountInTheCountsFileIsOwned(t *testing.T) {
	_, decls, err := readConstDecls(committedCounts)
	if err != nil {
		t.Fatalf("read %s: %v", committedCounts, err)
	}
	owned := map[string]struct{}{}
	for _, name := range derivedCountNames() {
		owned[name] = struct{}{}
	}

	integers := 0
	for _, d := range decls {
		if !d.IsInt {
			continue
		}
		integers++
		if _, ok := owned[d.Name]; ok {
			continue
		}
		if why, ok := refitNonDerivedCounts[d.Name]; ok {
			t.Logf("%s is not derived from the record: %s", d.Name, why)
			continue
		}
		t.Errorf("%s is an integer constant in %s that goldset-refit does not own. "+
			"Add it to derivedCounts so a refit keeps it true, or to refitNonDerivedCounts with the reason it is not derived from the record.",
			d.Name, committedCounts)
	}
	if integers == 0 {
		t.Fatalf("no integer constant was found in %s at all; a rename would turn this guard into a silent pass", committedCounts)
	}
	t.Logf("%d integer constants in %s, %d of them derived from the record", integers, committedCounts, len(derivedCounts))
}

// TestReadCountLiteralsRefusesWhatItCannotRewrite holds the property the whole
// mechanism rests on: a count goldset-refit silently skipped is a Gold Set change the
// diff does not show, which is the one thing the hard-coded counts exist to prevent.
func TestReadCountLiteralsRefusesWhatItCannotRewrite(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		want   []string
		refuse bool
	}{
		{
			name: "a well-formed control",
			src:  "package p\n\n// doc\nconst rows = 149\n",
			want: []string{"rows"},
		},
		{
			name:   "a name that is not there at all",
			src:    "package p\n\nconst other = 149\n",
			want:   []string{"rows"},
			refuse: true,
		},
		{
			name:   "a name declared twice",
			src:    "package p\n\nconst rows = 149\n\nconst (\n\trows = 150\n)\n",
			want:   []string{"rows"},
			refuse: true,
		},
		{
			name:   "an expression rather than a literal",
			src:    "package p\n\nconst rows = 3 + 4\n",
			want:   []string{"rows"},
			refuse: true,
		},
		{
			name:   "bound to another identifier",
			src:    "package p\n\nconst other = 149\nconst rows = other\n",
			want:   []string{"rows"},
			refuse: true,
		},
		{
			name:   "a float, which is a measurement rather than a count",
			src:    "package p\n\nconst rows = 0.5\n",
			want:   []string{"rows"},
			refuse: true,
		},
		{
			name:   "an implicit iota repetition carries no value to rewrite",
			src:    "package p\n\nconst (\n\tfirst = iota\n\trows\n)\n",
			want:   []string{"rows"},
			refuse: true,
		},
		{
			name:   "declared inside a function, which is a local alias and not the record",
			src:    "package p\n\nfunc f() int {\n\tconst rows = 149\n\treturn rows\n}\n",
			want:   []string{"rows"},
			refuse: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "counts.go")
			if err := os.WriteFile(path, []byte(tt.src), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			_, lits, err := readCountLiterals(path, tt.want)
			if tt.refuse {
				if err == nil {
					t.Fatalf("readCountLiterals accepted %q; it must refuse what it cannot rewrite", tt.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("readCountLiterals: %v", err)
			}
			if got := lits["rows"].Value; got != 149 {
				t.Errorf("read rows = %d, want 149", got)
			}
		})
	}
}

// TestRewriteCountsTouchesOnlyTheNumbers is the promise the verb makes to a reviewer
// reading the diff: the numbers move and nothing else does. The constants sit under
// long doc comments in three const blocks, and a rewrite that reflowed one of them
// would bury the change it exists to show.
func TestRewriteCountsTouchesOnlyTheNumbers(t *testing.T) {
	const src = `package p

// alpha is the first count.
//
// It carries a paragraph, because the real ones do.
const alpha = 12

const (
	// beta is the second.
	beta = 34
	// gamma is the third, and it does not move.
	gamma = 56
)
`
	path := filepath.Join(t.TempDir(), "counts.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	names := []string{"alpha", "beta", "gamma"}
	before, lits, err := readCountLiterals(path, names)
	if err != nil {
		t.Fatalf("readCountLiterals: %v", err)
	}

	changes := []countChange{
		{Name: "alpha", Old: 12, New: 7},
		{Name: "beta", Old: 34, New: 1234},
		{Name: "gamma", Old: 56, New: 56},
	}
	out, err := rewriteCounts(before, lits, changes)
	if err != nil {
		t.Fatalf("rewriteCounts: %v", err)
	}

	// The expectation is the INPUT with the two numbers substituted back, so any other
	// byte the rewriter touched -- a comment, a blank line, the untouched constant --
	// shows up as a mismatch rather than being absorbed into a re-render.
	want := strings.Replace(src, "const alpha = 12", "const alpha = 7", 1)
	want = strings.Replace(want, "beta = 34", "beta = 1234", 1)
	if !bytes.Equal(out, []byte(want)) {
		t.Errorf("the rewrite moved more than the numbers.\n got:\n%s\nwant:\n%s", out, want)
	}

	// And the result is still a file the compiler accepts, read back through the same
	// reader the verb would use on the next run.
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write rewritten: %v", err)
	}
	_, after, err := readCountLiterals(path, names)
	if err != nil {
		t.Fatalf("re-read the rewritten counts: %v", err)
	}
	for _, want := range []struct {
		name  string
		value int
	}{{"alpha", 7}, {"beta", 1234}, {"gamma", 56}} {
		if got := after[want.name].Value; got != want.value {
			t.Errorf("%s = %d after the rewrite, want %d", want.name, got, want.value)
		}
	}
}

// TestCountChangeRegressionIsTheRatchetsDirection makes ADR-0048's rule executable: a
// ratchet may only be moved in the direction that means a signature vanished by the
// person who withdraws the confirmation, and a pinned census may move either way
// because a drawing gaining rows and an ambiguity resolving are both legitimate.
func TestCountChangeRegressionIsTheRatchetsDirection(t *testing.T) {
	tests := []struct {
		name      string
		direction countDirection
		old, new  int
		want      bool
	}{
		{"a pending count falling is a confirmation landing", countMayFall, 188, 0, false},
		{"a pending count rising is a signature gone", countMayFall, 0, 3, true},
		{"a confirmed count rising is a confirmation landing", countMayRise, 4, 120, false},
		{"a confirmed count falling is a signature gone", countMayRise, 120, 119, true},
		{"a pinned census may move either way", countPinned, 188, 231, false},
		{"a pinned census may fall too", countPinned, 15, 10, false},
		{"a count that held still never regresses", countMayFall, 1, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := countChange{Name: "count", Direction: tt.direction, Old: tt.old, New: tt.new}
			if got := c.regression(); got != tt.want {
				t.Errorf("regression() = %v, want %v", got, tt.want)
			}
			if got, want := c.moved(), tt.old != tt.new; got != want {
				t.Errorf("moved() = %v, want %v", got, want)
			}
		})
	}
}
