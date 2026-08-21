// This file is llmbench's goldset-ui verb (ADR-0048, #286): a single embedded page
// served on loopback that walks the Extract Gold Set's unconfirmed rows one at a time,
// takes a Blind Confirmation on each, and writes them through goldset-apply and
// nothing else. It binds loopback only, refuses a foreign origin and requires a
// session token, because a tool that writes committed ground truth must not be
// reachable by a page the labeller happens to have open -- and it refuses to run under
// CI, because ground truth is only ever written by a person who chose to write it.
package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// goldSetUIPage is the whole labelling surface: one file, inline CSS and JS, no
// external asset and no build step, so the tool is `go run` and a browser tab.
//
//go:embed goldsetui.html
var goldSetUIPage []byte

// goldUIAuthority is the standing statement the screen carries. The labeller is
// reading the parsed page the live extract stage decided on, and their label is a
// statement about THAT (ADR-0043) -- a re-fetch months later is a different page.
const goldUIAuthority = "You are reading the captured content -- the parsed page the live extract stage decided on. Your label is a statement about it (ADR-0043)."

// goldUIAuthorityAid is what a measuring session adds to it. The captured content is
// the authority in BOTH sessions; when a re-fetch may be shown, the screen must also
// say that the live page is an aid and never the thing being labelled (ADR-0047).
const goldUIAuthorityAid = " A live re-fetch may be measured and shown beside it as an aid, and only while its Capture Fidelity admits it; it is never what your label is about (ADR-0047)."

// goldUIAuthorityFor is the standing statement for a session that does or does not
// admit a re-fetch, so what the labeller reads matches what the tool will show them.
func goldUIAuthorityFor(refetch bool) string {
	if !refetch {
		return goldUIAuthority
	}
	return goldUIAuthority + goldUIAuthorityAid
}

// goldUIMeasureWait bounds how long serving a row waits on its Capture Fidelity. Past
// it the row is served UNMEASURED and the screen says so: a slow host must not wedge
// the labelling pass (ADR-0047).
const goldUIMeasureWait = 25 * time.Second

// goldUIError is a session refusal that knows the HTTP status it means, so the
// handlers stay a thin translation of the session's own rules.
type goldUIError struct {
	status int
	msg    string
}

func (e goldUIError) Error() string { return e.msg }

// goldUIBadRequest refuses a request the labeller could fix by asking differently.
func goldUIBadRequest(msg string) error { return goldUIError{http.StatusBadRequest, msg} }

// goldUIConflict refuses a request aimed at a row the session has moved past -- a
// stale tab must never answer a different page than the one it is showing.
func goldUIConflict(msg string) error { return goldUIError{http.StatusConflict, msg} }

// goldUINotFound refuses a request for a row this sitting never drew.
func goldUINotFound(msg string) error { return goldUIError{http.StatusNotFound, msg} }

// goldUIStatus maps an error to its status; anything unclassified is a server fault
// (a journal that will not sync, an apply the verb refused).
func goldUIStatus(err error) int {
	var e goldUIError
	if errors.As(err, &e) {
		return e.status
	}
	return http.StatusInternalServerError
}

// runGoldSetUI serves the blind labelling tool over the Extract Gold Set. Exit: 2 on
// usage or an environment that may not confirm, 1 on IO or a final write that failed,
// 0 otherwise.
func runGoldSetUI(args []string) int {
	// First, before any flag is parsed and long before anything can listen: a gold-set
	// confirmation is a person's deliberate act, and an automated environment has no
	// person in it (ADR-0043).
	if os.Getenv("CI") != "" {
		fmt.Fprintln(os.Stderr, "llmbench goldset-ui: refusing to run under CI; a gold-set confirmation is a person's deliberate act, never an automated one (ADR-0043)")
		return 2
	}

	fs := flag.NewFlagSet("goldset-ui", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set")
	by := fs.String("by", "", "HUMAN name to sign every confirmation this session takes; required, and an \"llm:\" or \"script:\" name is rejected")
	stratum := fs.String("stratum", "", "restrict the pass to one stratum; empty serves every row still owed a confirmer")
	addr := fs.String("addr", goldUIDefaultAddr, "loopback address to serve on; a non-loopback address is refused")
	flushEvery := fs.Int("flush-every", goldUIDefaultFlushEvery, "write the buffered decisions to the record every N decisions")
	journal := fs.String("journal", "", "path for the session journal; empty writes it under the OS temp dir. A working artifact, never committed.")
	refetch := fs.Bool("refetch", true, "measure each row's Capture Fidelity by fetching its URL again, so a live view is admitted or refused per row (ADR-0047); -refetch=false labels from the captured content alone and offers no live view")
	refetchCache := fs.String("refetch-cache", "", "directory for the re-fetched bytes; empty uses <user cache dir>/"+goldRefetchCacheName+". A working artefact -- it must sit OUTSIDE the repository and is never committed (ADR-0047).")
	_ = fs.Parse(args)

	if strings.TrimSpace(*by) == "" {
		fmt.Fprintln(os.Stderr, "llmbench goldset-ui: -by is required; a confirmation is somebody's signature and never a default (ADR-0043)")
		return 2
	}
	if machineName(*by) {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: -by %q is not a human; confirmation cannot be automated (ADR-0043)\n", *by)
		return 2
	}
	if *stratum != "" && !goldStratum(*stratum).Valid() {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: -stratum %q is not a known stratum\n", *stratum)
		return 2
	}
	if *flushEvery < 1 {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: -flush-every must be >= 1, got %d\n", *flushEvery)
		return 2
	}
	if err := loopbackAddr(*addr); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: -addr %q: %v\n", *addr, err)
		return 2
	}

	journalPath := *journal
	if journalPath == "" {
		journalPath = filepath.Join(os.TempDir(), "llmbench-goldset-ui", time.Now().UTC().Format("20060102T150405Z")+".jsonl")
	}
	scratch, err := os.MkdirTemp("", "llmbench-goldset-ui-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: create the scratch directory: %v\n", err)
		return 1
	}

	// The re-fetch path is built before the session so a cache directory inside the
	// repository is refused before anything is read, listened on or journalled.
	var refetcher goldUIRefetcher
	cacheDir := ""
	if *refetch {
		resolved, err := goldRefetchCacheDir(*refetchCache, *dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-ui: -refetch-cache: %v\n", err)
			return 2
		}
		live, err := newLiveGoldRefetcher(resolved)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-ui: %v\n", err)
			return 1
		}
		refetcher, cacheDir = live, resolved
	}

	session, err := newGoldSetUISession(goldUIConfig{
		Dir: *dir, By: *by, Stratum: goldStratum(*stratum), FlushEvery: *flushEvery,
		JournalPath: journalPath, ScratchDir: scratch, Refetch: refetcher,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: %v\n", err)
		return 1
	}

	// Nothing to confirm is a finished job, not a failure, and it must not leave a
	// listener open on a port for a page that would have no row to show.
	if session.Queue() == 0 {
		if _, err := session.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-ui: %v\n", err)
			return 1
		}
		fmt.Printf("goldset-ui: nothing to confirm -- every row in %s already carries a confirmer\n", goldUIScope(goldStratum(*stratum)))
		return 0
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: listen on %s: %v (the port may already be taken; pass another with -addr)\n", *addr, err)
		return 1
	}

	fmt.Printf("goldset-ui: confirming as %q\n", *by)
	fmt.Printf("  gold set    %s\n", *dir)
	fmt.Printf("  queue       %d rows still owed a confirmer (%s)\n", session.Queue(), goldUIScope(goldStratum(*stratum)))
	fmt.Printf("  flush every %d decisions\n", *flushEvery)
	fmt.Printf("  journal     %s\n", journalPath)
	fmt.Printf("  scratch     %s\n", scratch)
	if refetcher != nil {
		fmt.Printf("  re-fetch    %s   (a working artefact; never committed, never written back onto a row)\n", cacheDir)
		fmt.Printf("  rendering   served from this tool, sandboxed and script-free; the site is never framed\n")
	} else {
		fmt.Printf("  re-fetch    off (-refetch=false); rows are labelled from the captured content alone\n")
	}
	fmt.Printf("  open        http://%s/?t=%s   (the token is this session's key -- do not paste it anywhere)\n", *addr, session.Token())

	srv := &http.Server{Handler: session.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The warm loop measures the rows ahead of the labeller, so a keystroke never waits
	// on a network fetch. It is started only after the listener is up, and stopped
	// before the final write, so no measurement lands while the buffer is flushed.
	warmCtx, stopWarm := context.WithCancel(context.Background())
	defer stopWarm()
	go session.WarmLoop(warmCtx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "llmbench goldset-ui: serve: %v\n", err)
			stopWarm()
			if _, cerr := session.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "llmbench goldset-ui: %v\n", cerr)
			}
			return 1
		}
	case <-ctx.Done():
	}

	// Shut the door before the last write: no decision may arrive while the buffer is
	// being flushed and the journal closed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: shutdown: %v\n", err)
	}
	stopWarm()
	written, err := session.Close()
	summary := session.Summary()
	fmt.Printf("\ngoldset-ui: %d decisions taken, %d written to the record (%d in the final batch)\n", summary.Decided, summary.Flushed, written)
	if summary.Comparable > 0 {
		fmt.Printf("  agreement   %d/%d (%.1f%%) (this labeller against the Proposed Label)\n",
			summary.Agreed, summary.Comparable, 100*float64(summary.Agreed)/float64(summary.Comparable))
	}
	fmt.Printf("  journal     %s\n", journalPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-ui: %v\n", err)
		return 1
	}
	return 0
}

// goldUIScope names the population a queue was drawn from, for the startup line.
func goldUIScope(s goldStratum) string {
	if s == "" {
		return "every stratum"
	}
	return "stratum " + string(s)
}

// loopbackAddr validates that addr binds loopback and nothing else. A gold set is
// committed ground truth; serving it on an interface anybody else can reach is not a
// mode this tool has. An empty host is refused explicitly, because ":7777" binds every
// interface and reads like a default.
func loopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("not a host:port address: %w", err)
	}
	if host == "" {
		return errors.New("an empty host binds every interface; name localhost explicitly")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return errors.New("only localhost or a loopback IP may be bound")
	}
	if !ip.IsLoopback() {
		return errors.New("is not a loopback address; this tool writes committed ground truth and never listens where somebody else can reach it")
	}
	return nil
}

// Handler is the session's whole HTTP surface, guard included, so a test drives
// exactly what a browser does and nothing can be reached around the front door.
func (s *goldSetUISession) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	// More specific than "GET /", so the page handler's 404 still covers "/render/"
	// with no id after it.
	mux.HandleFunc("GET /render/{id}", s.handleRender)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/next", s.handleNext)
	mux.HandleFunc("POST /api/answer", s.handleAnswer)
	mux.HandleFunc("POST /api/note", s.handleNote)
	mux.HandleFunc("POST /api/undo", s.handleUndo)
	mux.HandleFunc("POST /api/flush", s.handleFlush)
	return s.guard(mux)
}

// guard is the session's front door: a loopback Host, a same-origin request, and the
// session token. The token is the substantive check -- a page in the labeller's
// browser cannot guess 32 random bytes -- and the Host check is what stops a name that
// resolves to 127.0.0.1 from making that page same-origin in the first place.
func (s *goldSetUISession) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The token rides in the page URL, so nothing may carry it outward -- and #288
		// serves a rendering of a third-party page from this same origin, which is why
		// the frame gets no scripts and no same-origin.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")

		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !goldUILoopbackHost(host) {
			goldUIFail(w, http.StatusForbidden, "this tool answers only on loopback")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			goldUIFail(w, http.StatusForbidden, "a request from another origin cannot drive this tool")
			return
		}
		token := r.Header.Get("X-Goldset-Token")
		if token == "" {
			token = r.URL.Query().Get("t")
		}
		if !goldUITokenEqual(token, s.token) {
			goldUIFail(w, http.StatusUnauthorized, "this session's token is required; open the URL goldset-ui printed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// goldUILoopbackHost reports whether a request's Host names this machine only.
func goldUILoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// goldUITokenEqual compares a presented token with the session's in constant time,
// so a caller cannot learn the token a byte at a time from response timings.
func goldUITokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *goldSetUISession) handlePage(w http.ResponseWriter, r *http.Request) {
	// One page, one path: anything else under / is a typo, not a route.
	if r.URL.Path != "/" {
		goldUIFail(w, http.StatusNotFound, "no such page")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(goldSetUIPage)
}

// goldUIRenderCSP is the rendering document's own belt and braces. The iframe's
// sandbox is what disables scripts; this says the same thing at the wire, and it also
// stops the document reaching the network at all -- what is on screen is the crawler's
// parse and nothing a page could add to it. base-uri is deliberately left unset: the
// injected <base> is what resolves a link target the page wrote relative (#288).
const goldUIRenderCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'none'; frame-ancestors 'self'"

// handleRender serves one row's rendering into the labelling page's frame.
func (s *goldSetUISession) handleRender(w http.ResponseWriter, r *http.Request) {
	// HTML in every outcome, refusals included: the consumer is an iframe, and a JSON
	// body in a frame is a download prompt rather than a sentence.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", goldUIRenderCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	doc, err := s.RenderingDocument(r.PathValue("id"))
	if err != nil {
		w.WriteHeader(goldUIStatus(err))
		_, _ = w.Write(goldRenderRefusal(err.Error()))
		return
	}
	_, _ = w.Write(doc)
}

// goldUISessionInfo is what the page needs to describe the sitting: who is signing,
// what is being read, and the ONE question with the rubric both review surfaces state.
type goldUISessionInfo struct {
	By         string             `json:"by"`
	Dir        string             `json:"dir"`
	Stratum    goldStratum        `json:"stratum"`
	FlushEvery int                `json:"flush_every"`
	NoteRunes  int                `json:"note_runes"`
	Question   string             `json:"question"`
	Rubric     []labelRubricEntry `json:"rubric"`
	Authority  string             `json:"authority"`
	// Refetch reports that this session measures Capture Fidelity, so the page can
	// tell "not measured yet" from "no re-fetch is taken at all" (ADR-0047).
	Refetch bool `json:"refetch"`
}

func (s *goldSetUISession) handleSession(w http.ResponseWriter, r *http.Request) {
	goldUIJSON(w, http.StatusOK, goldUISessionInfo{
		By: s.cfg.By, Dir: s.cfg.Dir, Stratum: s.cfg.Stratum, FlushEvery: s.cfg.FlushEvery,
		NoteRunes: goldUINoteRunes, Question: extractLabelQuestion, Rubric: extractLabelRubric,
		Authority: goldUIAuthorityFor(s.refetch != nil), Refetch: s.refetch != nil,
	})
}

func (s *goldSetUISession) handleNext(w http.ResponseWriter, r *http.Request) {
	// The row on screen is measured before it is served, joining whatever fetch the
	// warm loop already has in flight. Bounded: past goldUIMeasureWait the row goes out
	// unmeasured, and the screen says so rather than the tab hanging.
	ctx, cancel := context.WithTimeout(r.Context(), goldUIMeasureWait)
	defer cancel()
	s.measureCurrent(ctx)
	goldUIJSON(w, http.StatusOK, goldUIResult{goldUIView: s.Current()})
}

func (s *goldSetUISession) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string             `json:"id"`
		Label bench.ExtractLabel `json:"label"`
	}
	if !goldUIDecode(w, r, &body) {
		return
	}
	result, err := s.Answer(body.ID, body.Label)
	if err != nil {
		goldUIFail(w, goldUIStatus(err), err.Error())
		return
	}
	goldUIJSON(w, http.StatusOK, result)
}

func (s *goldSetUISession) handleNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	}
	if !goldUIDecode(w, r, &body) {
		return
	}
	result, err := s.Note(body.ID, body.Note)
	if err != nil {
		goldUIFail(w, goldUIStatus(err), err.Error())
		return
	}
	goldUIJSON(w, http.StatusOK, result)
}

func (s *goldSetUISession) handleUndo(w http.ResponseWriter, r *http.Request) {
	result, err := s.Undo()
	if err != nil {
		goldUIFail(w, goldUIStatus(err), err.Error())
		return
	}
	goldUIJSON(w, http.StatusOK, result)
}

func (s *goldSetUISession) handleFlush(w http.ResponseWriter, r *http.Request) {
	result, err := s.Flush()
	if err != nil {
		goldUIFail(w, goldUIStatus(err), err.Error())
		return
	}
	goldUIJSON(w, http.StatusOK, result)
}

// goldUIRequestBytes bounds a request body. A note is at most goldUINoteRunes runes,
// so this is generous by three orders of magnitude and still finite.
const goldUIRequestBytes = 64 << 10

// goldUIDecode reads a bounded JSON body into v, reporting the fault itself when it
// cannot. It returns false when the caller must stop.
func goldUIDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, goldUIRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		goldUIFail(w, http.StatusBadRequest, fmt.Sprintf("unreadable request body: %v", err))
		return false
	}
	return true
}

func goldUIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func goldUIFail(w http.ResponseWriter, status int, msg string) {
	goldUIJSON(w, status, struct {
		Error string `json:"error"`
	}{msg})
}
