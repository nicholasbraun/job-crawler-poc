package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldSetUIPageBehaves drives the embedded page's OWN script -- goldsetui.html read
// off disk, not a copy -- over a miniature DOM, because the two things this page must
// never get wrong are behaviour rather than text:
//
//   - the frame must never show one row's page under another row's question. The label
//     is a statement about the row on screen (ADR-0043), so a stale rendering is the
//     one failure mode of the aid that corrupts a label rather than merely weakening it.
//   - a refusal must sit beside the field it is about and die with the decision it
//     belonged to, rather than lingering in a corner over a row it no longer applies to.
//
// The scenarios live in testdata/goldsetuipage/harness.mjs and name what they assert;
// the runner here reports whatever the harness printed.
func TestGoldSetUIPageBehaves(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		// node is the dashboard's build dependency, not the Go suite's, so its absence
		// is stated rather than failed -- but it does mean nothing checked the page.
		t.Skip("node is not on PATH: the page's own script was NOT exercised here (make web-build needs node too)")
	}
	harness := filepath.Join("testdata", "goldsetuipage", "harness.mjs")
	out, err := exec.CommandContext(t.Context(), node, harness, "goldsetui.html").CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("the labelling page failed its own scenarios (see the log above)")
		}
		t.Fatalf("run %s: %v", harness, err)
	}
}

// TestGoldSetUIPagePlacesItsRefusalAndItsCover reads the page's markup for the two
// placements the behaviour above depends on and that no DOM can assert for it: a
// refusal is only "beside the note field" if it sits in the note block, and a cover
// only covers if it is absolutely positioned over the frame's own wrapper.
func TestGoldSetUIPagePlacesItsRefusalAndItsCover(t *testing.T) {
	page := string(goldSetUIPage)

	at := func(t *testing.T, needle string) int {
		t.Helper()
		i := strings.Index(page, needle)
		if i < 0 {
			t.Fatalf("the page carries no %s", needle)
		}
		return i
	}
	inOrder := func(t *testing.T, why string, needles ...string) {
		t.Helper()
		prev := -1
		for _, needle := range needles {
			i := at(t, needle)
			if i < prev {
				t.Errorf("%s: %s comes too late in the page", why, needle)
			}
			prev = i
		}
	}

	t.Run("the refusal is in the note block", func(t *testing.T) {
		inOrder(t, "a refusal the labeller can fix belongs where they are typing",
			`id="notewrap"`, `<input id="note"`, `id="noteerror"`, `id="notecount"`, `id="advance"`)
	})

	t.Run("the cover is over the frame", func(t *testing.T) {
		inOrder(t, "the cover has to sit in the frame's own wrapper to cover it",
			`class="framewrap"`, `id="renderframe"`, `id="frameveil"`)
		for _, rule := range []string{".framewrap { position: relative; }", "position: absolute; inset: 0;"} {
			if !strings.Contains(page, rule) {
				t.Errorf("the page does not carry %q; a cover that is not positioned over the frame does not cover it", rule)
			}
		}
		// .hidden is a single class declared earlier, so an uncompounded .frameveil rule
		// would win over it and the cover would never lift.
		if !strings.Contains(page, ".frameveil.hidden { display: none; }") {
			t.Error("the cover has no rule that beats .hidden; it would never lift")
		}
	})
}

// TestGoldSetUIAnswersTheFaviconAheadOfTheGuard pins the one path the guard does not
// cover. The browser asks for a favicon on its own and carries no token when it does,
// so behind the guard it is a 401 and a console error in every session -- while the
// guard itself, which is what stops a page in the labeller's browser driving this tool,
// still covers everything else.
func TestGoldSetUIAnswersTheFaviconAheadOfTheGuard(t *testing.T) {
	session, _, ids := newTestSession(t, 10)
	srv := httptest.NewServer(session.Handler())
	t.Cleanup(srv.Close)

	// get sends exactly what a browser sends for a favicon: no token, no Origin.
	get := func(t *testing.T, method, path string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return res, string(body)
	}

	t.Run("the browser's own request is answered", func(t *testing.T) {
		res, body := get(t, http.MethodGet, "/favicon.ico")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /favicon.ico = %d, want 200: a token-less favicon request is a console error in every session", res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "image/svg+xml" {
			t.Errorf("Content-Type = %q, want image/svg+xml", ct)
		}
		if !strings.HasPrefix(body, "<svg") {
			t.Errorf("the favicon is not an SVG: %q", body)
		}
		if strings.Contains(strings.ToLower(body), "<script") {
			t.Errorf("the favicon carries a script: %q", body)
		}
		if strings.Contains(body, session.Token()) {
			t.Error("the favicon carries this session's token")
		}
	})

	t.Run("nothing else is answered without the token", func(t *testing.T) {
		// The exemption is one exact GET and no more: a path that merely starts with it,
		// and the same path under another method, are the guard's business.
		for _, tt := range []struct {
			method, path string
		}{
			{http.MethodGet, "/"},
			{http.MethodGet, "/api/session"},
			{http.MethodGet, "/api/next"},
			{http.MethodGet, "/render/" + ids["agree"]},
			{http.MethodGet, "/favicon.ico/x"},
			{http.MethodPost, "/favicon.ico"},
			{http.MethodPost, "/api/answer"},
		} {
			t.Run(tt.method+" "+tt.path, func(t *testing.T) {
				res, _ := get(t, tt.method, tt.path)
				if res.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s = %d without a token, want 401", tt.method, tt.path, res.StatusCode)
				}
			})
		}
	})
}
