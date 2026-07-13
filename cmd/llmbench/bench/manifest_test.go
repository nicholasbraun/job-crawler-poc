package bench_test

import (
	"errors"
	"os"
	"testing"
	"testing/fstest"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

func manifestFS(t *testing.T, json string) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"manifest.json": &fstest.MapFile{Data: []byte(json)},
	}
}

func TestLoadManifest(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
		invalid bool // expect errors.Is(err, ErrInvalidManifest)
	}{
		{
			name: "valid-two-entries",
			json: `[
				{"file":"a.html","url":"https://boards.greenhouse.io/acme","label":"career_page","category":"hub_ats_root","verified":true},
				{"file":"b.html","url":"https://www.linkedin.com/jobs/acme","label":"not_career_page","category":"aggregator","verified":false}
			]`,
		},
		{
			name:    "empty-file",
			json:    `[{"file":"","url":"https://x.example","label":"not_career_page","category":"unrelated"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "empty-url",
			json:    `[{"file":"a.html","url":"","label":"not_career_page","category":"unrelated"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "unknown-category",
			json:    `[{"file":"a.html","url":"https://x.example","label":"not_career_page","category":"foo"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "unknown-label",
			json:    `[{"file":"a.html","url":"https://x.example","label":"maybe","category":"unrelated"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "polarity-mismatch",
			json:    `[{"file":"a.html","url":"https://x.example","label":"career_page","category":"aggregator"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "malformed-json",
			json:    `{`,
			wantErr: true,
			invalid: false,
		},
		{
			name: "duplicate-file",
			json: `[
				{"file":"a.html","url":"https://x.example","label":"not_career_page","category":"unrelated"},
				{"file":"a.html","url":"https://y.example","label":"not_career_page","category":"unrelated"}
			]`,
			wantErr: true,
			invalid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := bench.LoadManifest(manifestFS(t, tt.json))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LoadManifest() = nil error, want error")
				}
				if tt.invalid && !errors.Is(err, bench.ErrInvalidManifest) {
					t.Fatalf("LoadManifest() error = %v, want errors.Is ErrInvalidManifest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadManifest() unexpected error: %v", err)
			}
			if len(m.Entries) != 2 {
				t.Fatalf("len(Entries) = %d, want 2", len(m.Entries))
			}
			e := m.Entries[0]
			if e.File != "a.html" || e.URL != "https://boards.greenhouse.io/acme" {
				t.Errorf("entry 0 = {File:%q URL:%q}, want a.html / greenhouse", e.File, e.URL)
			}
			if e.Label != bench.LabelCareerPage || e.Category != bench.CategoryHubATSRoot || !e.Verified {
				t.Errorf("entry 0 = {Label:%q Category:%q Verified:%v}, want career_page/hub_ats_root/true", e.Label, e.Category, e.Verified)
			}
			if m.Entries[1].Verified {
				t.Errorf("entry 1 Verified = true, want false")
			}
		})
	}
}

// TestLoadManifest_GateCertainAcceptOK checks the whitelist flag: it parses and
// sets the Entry field on a negative fixture, and is rejected (ErrInvalidManifest)
// on a positive one, since the whitelist only applies to negatives the gate
// certain-accepts.
func TestLoadManifest_GateCertainAcceptOK(t *testing.T) {
	t.Run("valid-on-negative", func(t *testing.T) {
		json := `[{"file":"a.html","url":"https://www.businessinsider.com/careers","label":"not_career_page","category":"unrelated","verified":true,"gate_certain_accept_ok":true}]`
		m, err := bench.LoadManifest(manifestFS(t, json))
		if err != nil {
			t.Fatalf("LoadManifest() error: %v", err)
		}
		if !m.Entries[0].GateCertainAcceptOK {
			t.Errorf("GateCertainAcceptOK = false, want true")
		}
	})

	t.Run("rejected-on-positive", func(t *testing.T) {
		json := `[{"file":"a.html","url":"https://boards.greenhouse.io/acme","label":"career_page","category":"hub_ats_root","gate_certain_accept_ok":true}]`
		if _, err := bench.LoadManifest(manifestFS(t, json)); !errors.Is(err, bench.ErrInvalidManifest) {
			t.Fatalf("LoadManifest() error = %v, want errors.Is ErrInvalidManifest", err)
		}
	})
}

func TestReadHTML(t *testing.T) {
	fsys := fstest.MapFS{
		"pages/x.html": &fstest.MapFile{Data: []byte("<title>x</title>")},
	}
	e := bench.Entry{File: "x.html"}
	got, err := e.ReadHTML(fsys)
	if err != nil {
		t.Fatalf("ReadHTML() error: %v", err)
	}
	if string(got) != "<title>x</title>" {
		t.Errorf("ReadHTML() = %q, want %q", got, "<title>x</title>")
	}
}

// TestLoadManifest_CommittedSet guards that the real testdata Gold Set parses,
// every fixture's HTML is present, and every category stratum is populated (the
// ADR-0008 / #50 stratification requirement). It stays dependency-light: no
// parser or gate runs here -- the real pipeline is exercised only by manual
// `go run`.
func TestLoadManifest_CommittedSet(t *testing.T) {
	fsys := os.DirFS("../testdata")
	m, err := bench.LoadManifest(fsys)
	if err != nil {
		t.Fatalf("LoadManifest(testdata) error: %v", err)
	}
	if len(m.Entries) == 0 {
		t.Fatal("committed Gold Set is empty")
	}
	counts := map[bench.Category]int{}
	for _, e := range m.Entries {
		counts[e.Category]++
		html, err := e.ReadHTML(fsys)
		if err != nil {
			t.Errorf("ReadHTML(%q) error: %v", e.File, err)
			continue
		}
		if len(html) == 0 {
			t.Errorf("ReadHTML(%q) returned empty bytes", e.File)
		}
	}
	// Each of the six strata must carry at least one fixture so every scorecard
	// slice has data to score.
	for _, c := range bench.AllCategories {
		if counts[c] == 0 {
			t.Errorf("category %q has no fixtures in the committed Gold Set", c)
		}
	}
}
