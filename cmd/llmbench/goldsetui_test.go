package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// goldUITestConfirmer is the human whose signature the tests drive the tool with. It
// is a fixture name and never touches the committed record: every test here works in a
// t.TempDir() gold set.
const goldUITestConfirmer = "A Human"

// goldUITestRows is the fixture gold set. The URLs are chosen so their row-id order
// -- which is the order the queue is served in, and which the tests assert -- is the
// reading order below; every row carries a SENTINEL so a leak into the blind view is
// unmistakable.
func goldUITestRows() []goldRow {
	proposer := goldProvenance{ProposedBy: "llm:SENTINEL-PROPOSER", ProposedAt: "2026-08-01T09:00:00Z", Note: "SENTINEL-PROPOSER-NOTE"}
	body := crawler.Content{
		Title:       "SENTINEL-TITLE",
		MainContent: "SENTINEL-BODY the role, what you bring, and how to apply.",
		JSONLD:      []string{`{"@type":"JobPosting","title":"SENTINEL-STRUCTURED"}`},
		URLs:        []string{"https://acme.test/jobs/one", "https://elsewhere.test/x"},
	}

	agree := proposer
	agree.ProposedLabel = bench.ExtractDetail
	disagree := proposer
	disagree.ProposedLabel = bench.ExtractResidue
	ambiguous := proposer
	ambiguous.ProposedLabel = bench.ExtractAmbiguous
	undo := proposer
	undo.ProposedLabel = bench.ExtractHubIndex
	confirmed := proposer
	confirmed.ConfirmedBy, confirmed.ConfirmedAt = "An Earlier Human", "2026-08-02T09:00:00Z"

	return []goldRow{
		{URL: "https://acme.test/jobs/agree", Verdict: true, TS: "2026-08-01T09:00:00Z", Stratum: stratumRandom, Weight: 1,
			Label: bench.ExtractDetail, LabelProvenance: agree, Content: body},
		{URL: "https://acme.test/jobs/disagree", Verdict: false, TS: "2026-08-01T09:00:01Z", Stratum: stratumRandom, Weight: 1,
			Label: bench.ExtractResidue, LabelProvenance: disagree, Content: body},
		{URL: "https://acme.test/jobs/ambiguous", Verdict: true, TS: "2026-08-01T09:00:02Z", Stratum: stratumBoundary, Weight: 1,
			Label: bench.ExtractAmbiguous, LabelProvenance: ambiguous, Content: body},
		{URL: "https://acme.test/jobs/unlabelled", Verdict: false, TS: "2026-08-01T09:00:03Z", Stratum: stratumRandom, Weight: 1,
			Content: body},
		{URL: "https://acme.test/jobs/undo", Verdict: true, TS: "2026-08-01T09:00:04Z", Stratum: stratumRandom, Weight: 1,
			Label: bench.ExtractHubIndex, LabelProvenance: undo, Content: body},
		{URL: "https://acme.test/jobs/confirmed", Verdict: true, TS: "2026-08-01T09:00:05Z", Stratum: stratumRandom, Weight: 1,
			Label: bench.ExtractDetail, LabelProvenance: confirmed, Content: body},
	}
}

// newTestGoldSet writes the fixture rows into a temp directory through the real
// writer, so the tests read and write the same three files goldset-apply does.
func newTestGoldSet(t *testing.T) (dir string, ids map[string]string) {
	t.Helper()
	dir = t.TempDir()
	rows := goldUITestRows()
	if err := writeGoldSetFiles(dir, rows); err != nil {
		t.Fatalf("writeGoldSetFiles: %v", err)
	}
	ids = map[string]string{}
	for _, row := range rows {
		ids[strings.TrimPrefix(row.URL, "https://acme.test/jobs/")] = rowID(row.URL)
	}
	return dir, ids
}

// newTestSession builds a session over a fresh fixture gold set, with the journal and
// the batch scratch files under t.TempDir() -- never the repository.
func newTestSession(t *testing.T, flushEvery int) (*goldSetUISession, goldUIConfig, map[string]string) {
	t.Helper()
	dir, ids := newTestGoldSet(t)
	cfg := goldUIConfig{
		Dir: dir, By: goldUITestConfirmer, FlushEvery: flushEvery,
		JournalPath: filepath.Join(t.TempDir(), "journal.jsonl"),
		ScratchDir:  t.TempDir(),
	}
	session, err := newGoldSetUISession(cfg)
	if err != nil {
		t.Fatalf("newGoldSetUISession: %v", err)
	}
	t.Cleanup(func() { _ = session.journal.Close() })
	return session, cfg, ids
}

// uiResp is the wire shape every handler answers with, decoded loosely so a test can
// assert on the fields it cares about.
type uiResp struct {
	Done     bool `json:"done"`
	Progress struct {
		Position, Total, Decided, Buffered, Flushed, Agreed, Comparable int
	} `json:"progress"`
	Question *struct {
		ID           string `json:"id"`
		URL          string `json:"url"`
		Title        string `json:"title"`
		Head         string `json:"head"`
		Mid          string `json:"mid"`
		URLsTotal    int    `json:"urls_total"`
		URLsSameHost int    `json:"urls_same_host"`
		URLsJoblike  int    `json:"urls_joblike"`
		Fidelity     *struct {
			State     string  `json:"state"`
			Status    int     `json:"status"`
			Retention float64 `json:"retention"`
			LiveView  bool    `json:"live_view"`
			Reason    string  `json:"reason"`
			At        string  `json:"at"`
		} `json:"fidelity"`
	} `json:"question"`
	Reveal *struct {
		ProposedLabel string `json:"proposed_label"`
		ProposedBy    string `json:"proposed_by"`
		ProposerNote  string `json:"proposer_note"`
		CurrentLabel  string `json:"current_label"`
		Stratum       string `json:"stratum"`
		Verdict       bool   `json:"verdict"`
		Chosen        string `json:"chosen"`
		Agreed        bool   `json:"agreed"`
		Comparable    bool   `json:"comparable"`
		NoteRequired  bool   `json:"note_required"`
	} `json:"reveal"`
	Pending *struct {
		Label string `json:"label"`
	} `json:"pending"`
	Recorded bool   `json:"recorded"`
	Flushed  int    `json:"flushed"`
	Outcome  string `json:"outcome"`
	ID       string `json:"id"`
	Error    string `json:"error"`
}

// uiClient drives the session over HTTP exactly as the page does: the session token
// in a header and a same-origin request, through httptest so no test ever binds a
// fixed port.
type uiClient struct {
	t     *testing.T
	base  string
	token string
}

func newUIClient(t *testing.T, session *goldSetUISession) *uiClient {
	t.Helper()
	srv := httptest.NewServer(session.Handler())
	t.Cleanup(srv.Close)
	return &uiClient{t: t, base: srv.URL, token: session.Token()}
}

func (c *uiClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Goldset-Token", c.token)
	req.Header.Set("Origin", c.base)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw := new(bytes.Buffer)
	if _, err := raw.ReadFrom(res.Body); err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return res.StatusCode, raw.Bytes()
}

// ok performs a request and fails the test unless it succeeded.
func (c *uiClient) ok(method, path string, body any) uiResp {
	c.t.Helper()
	status, raw := c.do(method, path, body)
	if status != http.StatusOK {
		c.t.Fatalf("%s %s = %d: %s", method, path, status, raw)
	}
	var out uiResp
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("decode %s %s: %v (%s)", method, path, err, raw)
	}
	return out
}

// recordOf reads the committed record back keyed by row id, through the same reader
// every other verb uses.
func recordOf(t *testing.T, dir string) map[string]goldRow {
	t.Helper()
	substrate, _ := goldSetPaths(dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	byID := map[string]goldRow{}
	for _, row := range rows {
		byID[rowID(row.URL)] = row
	}
	return byID
}

// sheetOf reads labels.tsv back keyed by row id, so a test can assert the review
// surface and the substrate agree about who confirmed what.
func sheetOf(t *testing.T, dir string) map[string]sheetRow {
	t.Helper()
	_, path := goldSetPaths(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	rows, err := parseSheet(data)
	if err != nil {
		t.Fatalf("parseSheet: %v", err)
	}
	byID := map[string]sheetRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID
}

// TestGoldSetUIWritesTheRecordThroughTheApplyVerb is the seam #286 names: the
// buffer, the argument synthesis, goldset-apply and its retraction rules driven end to
// end over a temporary gold set with no mocks. Every phase asserts the RECORD, not the
// tool's own account of it, because the record is the only thing that survives the
// session.
func TestGoldSetUIWritesTheRecordThroughTheApplyVerb(t *testing.T) {
	session, cfg, ids := newTestSession(t, 2)
	client := newUIClient(t, session)

	// -- resume: the queue is exactly the rows still owed a confirmer, in row-id order.
	if got := client.ok(http.MethodPost, "/api/undo", struct{}{}).Outcome; got != "nothing" {
		t.Errorf("undo on an untouched session = %q, want %q", got, "nothing")
	}
	view := client.ok(http.MethodGet, "/api/next", nil)
	if view.Progress.Total != 5 {
		t.Fatalf("the queue holds %d rows, want the 5 unconfirmed ones -- a row already carrying a confirmer must never cost a keystroke", view.Progress.Total)
	}
	if view.Progress.Position != 1 || view.Question == nil || view.Question.ID != ids["agree"] {
		t.Fatalf("the first row served is %+v, want position 1 and row %s", view.Question, ids["agree"])
	}

	// -- blindness: the wire itself carries nothing that answers the question.
	_, raw := client.do(http.MethodGet, "/api/next", nil)
	for _, leak := range []string{"SENTINEL-STRUCTURED", "SENTINEL-PROPOSER-NOTE", "JobPosting", "proposed", "stratum", "verdict", "label", "weight", "renderer", "expected"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(strings.ToLower(leak))) {
			t.Errorf("the blind view carries %q, which would anchor the answer it is asking for (ADR-0048): %s", leak, raw)
		}
	}
	for _, want := range []string{"SENTINEL-BODY", "SENTINEL-TITLE", ids["agree"]} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("the blind view withholds %q, which the labeller needs to answer at all", want)
		}
	}

	// -- agreement: one keystroke, no note, and the proposer's own note survives.
	answered := client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["agree"], "label": "detail"})
	if answered.Reveal == nil || answered.Reveal.ProposedLabel != "detail" || !answered.Reveal.Agreed {
		t.Fatalf("answering the agreeing row revealed %+v, want the proposed label detail and agreement", answered.Reveal)
	}
	if answered.Reveal.NoteRequired || !answered.Recorded {
		t.Errorf("an agreement asked for a note (%v) or was not recorded (%v)", answered.Reveal.NoteRequired, answered.Recorded)
	}
	if status, body := client.do(http.MethodPost, "/api/note", map[string]string{"id": ids["agree"], "note": "unwanted"}); status != http.StatusBadRequest {
		t.Errorf("a note on an agreement = %d (%s), want 400: it would overwrite the proposer's own", status, body)
	}

	// -- disagreement: the note is owed, and until it lands the decision is not one.
	view = client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil || view.Question.ID != ids["disagree"] {
		t.Fatalf("the second row served is %+v, want %s", view.Question, ids["disagree"])
	}
	answered = client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["disagree"], "label": "detail"})
	if answered.Reveal == nil || answered.Reveal.Agreed || !answered.Reveal.NoteRequired {
		t.Fatalf("disagreeing revealed %+v, want disagreement and a required note", answered.Reveal)
	}
	if answered.Recorded {
		t.Error("a decision still owing its note was recorded; it is not yet a decision")
	}
	if answered.Progress.Buffered != 1 {
		t.Errorf("the buffer holds %d decisions with an answer still pending, want only the completed one", answered.Progress.Buffered)
	}
	noted := client.ok(http.MethodPost, "/api/note", map[string]string{"id": ids["disagree"], "note": "the role body is a full posting"})
	if !noted.Recorded || noted.Flushed != 2 {
		t.Fatalf("completing the second decision recorded=%v flushed=%d, want the batch of 2 written at the flush-every boundary", noted.Recorded, noted.Flushed)
	}

	// -- the record: exactly what a blind pass is supposed to leave behind.
	record := recordOf(t, cfg.Dir)
	agreeRow := record[ids["agree"]]
	if agreeRow.Label != bench.ExtractDetail || agreeRow.LabelProvenance.ProposedLabel != bench.ExtractDetail {
		t.Errorf("the agreed row reads label %q / proposed %q, want detail / detail", agreeRow.Label, agreeRow.LabelProvenance.ProposedLabel)
	}
	if agreeRow.LabelProvenance.Note != "SENTINEL-PROPOSER-NOTE" {
		t.Errorf("the agreed row's note is %q, want the proposer's own left untouched", agreeRow.LabelProvenance.Note)
	}
	if agreeRow.LabelProvenance.ConfirmedBy != goldUITestConfirmer {
		t.Errorf("the agreed row is confirmed by %q, want %q", agreeRow.LabelProvenance.ConfirmedBy, goldUITestConfirmer)
	}
	if _, err := time.Parse(time.RFC3339, agreeRow.LabelProvenance.ConfirmedAt); err != nil {
		t.Errorf("confirmed_at %q does not parse as RFC3339: %v", agreeRow.LabelProvenance.ConfirmedAt, err)
	}
	disagreeRow := record[ids["disagree"]]
	if disagreeRow.Label != bench.ExtractDetail {
		t.Errorf("the overridden row reads label %q, want the human's detail", disagreeRow.Label)
	}
	if disagreeRow.LabelProvenance.ProposedLabel != bench.ExtractResidue {
		t.Errorf("the overridden row's proposed label is %q, want the proposer's residue preserved (ADR-0048)", disagreeRow.LabelProvenance.ProposedLabel)
	}
	if disagreeRow.LabelProvenance.Note != "the role body is a full posting" {
		t.Errorf("the overridden row's note is %q, want the human's replacing the proposer's", disagreeRow.LabelProvenance.Note)
	}
	if disagreeRow.LabelProvenance.ConfirmedBy != goldUITestConfirmer {
		t.Errorf("the overridden row is confirmed by %q, want %q", disagreeRow.LabelProvenance.ConfirmedBy, goldUITestConfirmer)
	}
	if got := record[ids["confirmed"]].LabelProvenance.ConfirmedBy; got != "An Earlier Human" {
		t.Errorf("a row nobody in this session read is confirmed by %q, want %q untouched", got, "An Earlier Human")
	}
	sheet := sheetOf(t, cfg.Dir)
	for id, row := range record {
		if sheet[id].ConfirmedBy != row.LabelProvenance.ConfirmedBy {
			t.Errorf("row %s: labels.tsv says %q confirmed it, the substrate says %q", id, sheet[id].ConfirmedBy, row.LabelProvenance.ConfirmedBy)
		}
	}

	// -- ambiguous: agreement still owes a note, because the note is what records the
	// tension that made the page unresolvable.
	view = client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil || view.Question.ID != ids["ambiguous"] {
		t.Fatalf("the third row served is %+v, want %s", view.Question, ids["ambiguous"])
	}
	answered = client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["ambiguous"], "label": "ambiguous"})
	if answered.Reveal == nil || !answered.Reveal.Agreed || !answered.Reveal.NoteRequired {
		t.Fatalf("an agreeing ambiguous answer revealed %+v, want agreement AND a required note", answered.Reveal)
	}
	client.ok(http.MethodPost, "/api/note", map[string]string{"id": ids["ambiguous"], "note": "a posting and a board on one page"})

	// -- undo an unflushed decision: it is taken back, its row is re-served, and the
	// record never hears about it. Blindness stays spent.
	undone := client.ok(http.MethodPost, "/api/undo", struct{}{})
	if undone.Outcome != "undone" || undone.Progress.Buffered != 0 {
		t.Fatalf("undoing a buffered decision = %q with %d still buffered, want %q and 0", undone.Outcome, undone.Progress.Buffered, "undone")
	}
	view = client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil || view.Question.ID != ids["ambiguous"] {
		t.Fatalf("after an undo the row served is %+v, want the undone row %s back", view.Question, ids["ambiguous"])
	}
	if view.Reveal == nil || view.Reveal.Chosen != "" {
		t.Errorf("the re-served row reads %+v, want its reveal still attached and no answer held: blindness, once spent, is spent", view.Reveal)
	}
	if flushed := client.ok(http.MethodPost, "/api/flush", struct{}{}).Flushed; flushed != 0 {
		t.Errorf("flushing after an undo wrote %d decisions, want 0", flushed)
	}
	if got := recordOf(t, cfg.Dir)[ids["ambiguous"]].LabelProvenance.ConfirmedBy; got != "" {
		t.Errorf("the undone row is confirmed by %q on the record, want nobody", got)
	}

	// -- the unlabelled row: the human's label lands through the proposals path in the
	// same invocation that confirms it, and manufactures no self-agreement.
	client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["ambiguous"], "label": "ambiguous"})
	client.ok(http.MethodPost, "/api/note", map[string]string{"id": ids["ambiguous"], "note": "a posting and a board on one page"})
	view = client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil || view.Question.ID != ids["unlabelled"] {
		t.Fatalf("the fourth row served is %+v, want %s", view.Question, ids["unlabelled"])
	}
	answered = client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["unlabelled"], "label": "detail"})
	if answered.Reveal == nil || answered.Reveal.Comparable || !answered.Reveal.NoteRequired {
		t.Fatalf("answering a row with no Proposed Label revealed %+v, want it incomparable and a note required", answered.Reveal)
	}
	noted = client.ok(http.MethodPost, "/api/note", map[string]string{"id": ids["unlabelled"], "note": "one role, one apply button"})
	if noted.Flushed != 2 {
		t.Fatalf("the batch wrote %d decisions, want 2", noted.Flushed)
	}
	record = recordOf(t, cfg.Dir)
	fresh := record[ids["unlabelled"]]
	if fresh.Label != bench.ExtractDetail || fresh.LabelProvenance.ConfirmedBy != goldUITestConfirmer {
		t.Errorf("the previously unlabelled row reads label %q confirmed by %q, want detail confirmed by %q", fresh.Label, fresh.LabelProvenance.ConfirmedBy, goldUITestConfirmer)
	}
	if fresh.LabelProvenance.ProposedBy != "" || fresh.LabelProvenance.ProposedLabel != "" {
		t.Errorf("the previously unlabelled row names proposer %q / proposed %q, want both empty: stamping the labeller as the proposer would make the agreement rate measure them against themselves",
			fresh.LabelProvenance.ProposedBy, fresh.LabelProvenance.ProposedLabel)
	}
	if got := record[ids["ambiguous"]].Label; got != bench.ExtractAmbiguous {
		t.Errorf("the re-answered row reads label %q, want ambiguous", got)
	}
	rows := make([]goldRow, 0, len(record))
	for _, row := range record {
		rows = append(rows, row)
	}
	if agreed, comparable := labelAgreement(rows); comparable != 3 || agreed != 2 {
		t.Errorf("labelAgreement = %d/%d, want 2/3: the three confirmed rows carrying a Proposed Label, one of them overridden", agreed, comparable)
	}

	// -- relabel a written decision: goldset-apply retracts the confirmation whose
	// label changed and re-stamps it in ONE invocation.
	view = client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil || view.Question.ID != ids["undo"] {
		t.Fatalf("the fifth row served is %+v, want %s", view.Question, ids["undo"])
	}
	client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["undo"], "label": "hub-index"})
	if flushed := client.ok(http.MethodPost, "/api/flush", struct{}{}).Flushed; flushed != 1 {
		t.Fatalf("writing the buffer on demand wrote %d decisions, want 1", flushed)
	}
	firstStamp := recordOf(t, cfg.Dir)[ids["undo"]].LabelProvenance.ConfirmedAt
	relabel := client.ok(http.MethodPost, "/api/undo", struct{}{})
	if relabel.Outcome != "relabel" || relabel.ID != ids["undo"] {
		t.Fatalf("undoing a written decision = %q on %q, want %q on %s: a signed act is changed by relabelling, not erased", relabel.Outcome, relabel.ID, "relabel", ids["undo"])
	}
	client.ok(http.MethodPost, "/api/answer", map[string]string{"id": ids["undo"], "label": "residue"})
	client.ok(http.MethodPost, "/api/note", map[string]string{"id": ids["undo"], "note": "a benefits page, not a board"})
	if flushed := client.ok(http.MethodPost, "/api/flush", struct{}{}).Flushed; flushed != 1 {
		t.Fatalf("writing the relabel wrote %d decisions, want 1", flushed)
	}
	relabelled := recordOf(t, cfg.Dir)[ids["undo"]]
	if relabelled.Label != bench.ExtractResidue {
		t.Errorf("the relabelled row reads label %q, want residue", relabelled.Label)
	}
	if relabelled.LabelProvenance.ConfirmedBy != goldUITestConfirmer {
		t.Errorf("the relabelled row is confirmed by %q, want %q re-stamped in the same invocation that retracted it", relabelled.LabelProvenance.ConfirmedBy, goldUITestConfirmer)
	}
	if relabelled.LabelProvenance.ConfirmedAt < firstStamp {
		t.Errorf("confirmed_at went backwards: %q, was %q", relabelled.LabelProvenance.ConfirmedAt, firstStamp)
	}
	if relabelled.LabelProvenance.ProposedLabel != bench.ExtractHubIndex {
		t.Errorf("the relabelled row's proposed label is %q, want the proposer's hub-index preserved", relabelled.LabelProvenance.ProposedLabel)
	}
	if relabelled.LabelProvenance.Note != "a benefits page, not a board" {
		t.Errorf("the relabelled row's note is %q, want the human's", relabelled.LabelProvenance.Note)
	}

	// -- the queue is done, and the journal is the account of how it got here.
	view = client.ok(http.MethodGet, "/api/next", nil)
	if !view.Done {
		t.Errorf("the queue is not done after every row was decided: %+v", view)
	}
	decisions, flushes := 0, 0
	data, err := os.ReadFile(cfg.JournalPath)
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry goldUIJournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("the journal holds an unreadable line %q: %v", line, err)
		}
		switch entry.Event {
		case "decision":
			decisions++
		case "flush":
			flushes++
		}
	}
	// Seven completed decisions: five rows, one of them decided three times (undone,
	// re-answered) and one relabelled after its confirmation was written.
	if decisions != 7 {
		t.Errorf("the journal holds %d decisions, want 7 -- every completed decision is journalled the moment it is taken", decisions)
	}
	if flushes != 4 {
		t.Errorf("the journal holds %d flushes, want 4", flushes)
	}
}

// TestGoldSetUIApplyArgsAreTheApplyVerbsOwn pins this tool's ONLY write path: the
// argument list it hands goldset-apply. The two flags it must never synthesize are
// asserted by name, because either one silently corrupts the number a blind pass
// exists to produce.
func TestGoldSetUIApplyArgsAreTheApplyVerbsOwn(t *testing.T) {
	tests := []struct {
		name      string
		proposals string
		want      []string
	}{
		{
			name: "a batch of pure agreements carries no proposals file",
			want: []string{"-dir=/gold", "-confirmed-by=A Human", "-confirm-ids=/scratch/confirmed-01.txt"},
		},
		{
			name:      "a batch carrying an override travels both paths in one invocation",
			proposals: "/scratch/proposals-01.tsv",
			want: []string{"-dir=/gold", "-proposals=/scratch/proposals-01.tsv", "-confirmed-by=A Human",
				"-confirm-ids=/scratch/confirmed-01.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goldUIApplyArgs("/gold", "A Human", tt.proposals, "/scratch/confirmed-01.txt")
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("goldUIApplyArgs = %v, want %v", got, tt.want)
			}
			for _, arg := range got {
				if !strings.HasPrefix(arg, "-") || !strings.Contains(arg, "=") {
					t.Errorf("argument %q is not in -flag=value form; a confirmer's name must never be readable as a flag", arg)
				}
				if strings.HasPrefix(arg, "-proposed-by") {
					t.Error("-proposed-by would stamp the labeller as the proposer of their own label, and the agreement rate would then measure them against themselves (ADR-0048)")
				}
				if strings.HasPrefix(arg, "-confirm-stratum") {
					t.Error("-confirm-stratum is mutually exclusive with the id list, and the id list is what makes the batch exact")
				}
				if strings.HasPrefix(arg, "-now") {
					t.Error("-now would freeze the record's timestamp; the journal carries the per-decision instant")
				}
			}
		})
	}
}

// TestGoldUINoteRule pins ADR-0048's note rule, which is the whole reason an answer
// and its note are two steps.
func TestGoldUINoteRule(t *testing.T) {
	row := func(proposed bench.ExtractLabel) goldRow {
		return goldRow{URL: "https://acme.test/x", LabelProvenance: goldProvenance{ProposedLabel: proposed}}
	}
	tests := []struct {
		name     string
		row      goldRow
		chosen   bench.ExtractLabel
		wantNote bool
	}{
		{"agreement leaves the proposer's note alone", row(bench.ExtractDetail), bench.ExtractDetail, false},
		{"disagreement is where the proposer is wrong, so it is argued", row(bench.ExtractDetail), bench.ExtractHubIndex, true},
		{"an agreeing ambiguous still records what the tension was", row(bench.ExtractAmbiguous), bench.ExtractAmbiguous, true},
		{"no Proposed Label leaves no proposer's argument to fall back on", row(""), bench.ExtractDetail, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := goldUINoteRequired(tt.row, tt.chosen); got != tt.wantNote {
				t.Errorf("goldUINoteRequired = %v, want %v", got, tt.wantNote)
			}
		})
	}
}

// TestGoldUIProposalLinesOmitAnAgreement pins what a batch does and does not propose.
// An agreement that emitted a line would overwrite the proposer's note with an empty
// one; a note carrying a tab would split a labels.tsv line.
func TestGoldUIProposalLinesOmitAnAgreement(t *testing.T) {
	agreements := []goldUIDecision{
		{ID: "bbb", Label: bench.ExtractDetail},
		{ID: "aaa", Label: bench.ExtractResidue},
	}
	if got := goldUIProposalLines("A Human", "2026-08-21T10:00:00Z", agreements); got != nil {
		t.Errorf("a batch of pure agreements proposed %q, want no proposals file at all", got)
	}

	batch := []goldUIDecision{
		{ID: "ccc", Label: bench.ExtractDetail, Relabel: true, Note: "a real\tposting\nbody"},
		{ID: "aaa", Label: bench.ExtractResidue},
		{ID: "bbb", Label: bench.ExtractHubIndex, Relabel: true},
		{ID: "ccc", Label: bench.ExtractHubIndex, Relabel: true, Note: "on reflection a board"},
	}
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(goldUIProposalLines("A Human", "2026-08-21T10:00:00Z", batch))), "\n") {
		if !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	want := []string{"bbb\thub-index", "ccc\thub-index\ton reflection a board"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("proposals = %v, want %v (sorted by id, last decision winning, agreements omitted)", lines, want)
	}
	// The synthesized file must survive the parser goldset-apply reads it with.
	parsed, err := parseProposals(goldUIProposalLines("A Human", "2026-08-21T10:00:00Z", batch))
	if err != nil {
		t.Fatalf("parseProposals over the synthesized file: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parseProposals read %d rows, want 2", len(parsed))
	}

	flattened := goldUIProposalLines("A Human", "2026-08-21T10:00:00Z", []goldUIDecision{
		{ID: "aaa", Label: bench.ExtractDetail, Relabel: true, Note: "a real\tposting\nbody"},
	})
	if !bytes.Contains(flattened, []byte("aaa\tdetail\ta real posting body\n")) {
		t.Errorf("a note carrying a tab and a newline rendered as %q, want it flattened onto one line", flattened)
	}

	ids := goldUIConfirmIDLines("A Human", "2026-08-21T10:00:00Z", batch)
	parsedIDs, err := parseConfirmIDs(ids)
	if err != nil {
		t.Fatalf("parseConfirmIDs over the synthesized file: %v", err)
	}
	if len(parsedIDs) != 3 {
		t.Errorf("the confirmation list names %d rows, want the 3 distinct rows the batch decided", len(parsedIDs))
	}
}

// TestGoldSetUIQueueOrderIsIndependentOfTheLabel is the property that keeps a pass
// from being read label-first: the order is a hash of the URL, so it cannot group the
// rows by the answer being checked.
func TestGoldSetUIQueueOrderIsIndependentOfTheLabel(t *testing.T) {
	session, _, _ := newTestSession(t, 10)
	order := []string{}
	for _, q := range session.queue {
		order = append(order, rowID(q.row.URL))
	}
	if !sort.StringsAreSorted(order) {
		t.Errorf("the queue is served in %v, want ascending row-id order", order)
	}
	if len(order) != 5 {
		t.Fatalf("the queue holds %d rows, want the 5 unconfirmed ones", len(order))
	}

	// Relabelling every row must not move a single one of them.
	dir := t.TempDir()
	rows := goldUITestRows()
	for i := range rows {
		if rows[i].Label.Valid() {
			rows[i].Label = bench.ExtractHubIndex
			rows[i].LabelProvenance.ProposedLabel = bench.ExtractHubIndex
		}
	}
	if err := writeGoldSetFiles(dir, rows); err != nil {
		t.Fatalf("writeGoldSetFiles: %v", err)
	}
	relabelled, err := newGoldSetUISession(goldUIConfig{
		Dir: dir, By: goldUITestConfirmer, FlushEvery: 10,
		JournalPath: filepath.Join(t.TempDir(), "journal.jsonl"), ScratchDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("newGoldSetUISession: %v", err)
	}
	defer relabelled.journal.Close()
	after := []string{}
	for _, q := range relabelled.queue {
		after = append(after, rowID(q.row.URL))
	}
	if strings.Join(after, " ") != strings.Join(order, " ") {
		t.Errorf("changing every label reordered the queue: %v, was %v", after, order)
	}
}

// TestGoldSetUIServesOneStratum pins the flag that makes a pass finishable: the queue
// is the rows of one stratum still owed a confirmer, and nothing else.
func TestGoldSetUIServesOneStratum(t *testing.T) {
	dir, ids := newTestGoldSet(t)
	session, err := newGoldSetUISession(goldUIConfig{
		Dir: dir, By: goldUITestConfirmer, Stratum: stratumBoundary, FlushEvery: 10,
		JournalPath: filepath.Join(t.TempDir(), "journal.jsonl"), ScratchDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("newGoldSetUISession: %v", err)
	}
	defer session.journal.Close()
	if len(session.queue) != 1 || rowID(session.queue[0].row.URL) != ids["ambiguous"] {
		t.Fatalf("the boundary queue holds %d rows, want only %s", len(session.queue), ids["ambiguous"])
	}
}

// TestGoldSetUIRefusesANameThatIsNotAHuman keeps the one thing a confirmation is: a
// person's signature (ADR-0043).
func TestGoldSetUIRefusesANameThatIsNotAHuman(t *testing.T) {
	dir, _ := newTestGoldSet(t)
	for _, name := range []string{"", "   ", "llm:claude-opus-5", "script:propose-expected"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			_, err := newGoldSetUISession(goldUIConfig{
				Dir: dir, By: name, FlushEvery: 1,
				JournalPath: filepath.Join(t.TempDir(), "journal.jsonl"), ScratchDir: t.TempDir(),
			})
			if err == nil {
				t.Fatalf("a session opened signing as %q; a confirmation cannot be automated or anonymous", name)
			}
		})
	}
}

// TestGoldSetUIBindsLoopbackOnly is the reachability rule: a tool that writes
// committed ground truth has no mode in which somebody else can reach it.
func TestGoldSetUIBindsLoopbackOnly(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"localhost:7777", true},
		{"127.0.0.1:0", true},
		{"[::1]:7777", true},
		{"127.0.0.53:7777", true},
		{":7777", false},
		{"0.0.0.0:7777", false},
		{"192.168.1.10:7777", false},
		{"example.com:80", false},
		{"garbage", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := loopbackAddr(tt.addr)
			if tt.ok && err != nil {
				t.Errorf("loopbackAddr(%q) = %v, want it accepted", tt.addr, err)
			}
			if !tt.ok && err == nil {
				t.Errorf("loopbackAddr(%q) was accepted; it reaches beyond this machine", tt.addr)
			}
		})
	}
}

// TestGoldSetUIRefusesAForeignCaller drives the guard over the real handler, which is
// what a browser reaches: a page the labeller happens to have open must not be able to
// drive the tool that writes the record.
func TestGoldSetUIRefusesAForeignCaller(t *testing.T) {
	session, _, _ := newTestSession(t, 10)
	srv := httptest.NewServer(session.Handler())
	t.Cleanup(srv.Close)

	tests := []struct {
		name   string
		token  string
		origin string
		host   string
		want   int
	}{
		{name: "no token", want: http.StatusUnauthorized},
		{name: "a guessed token", token: "0000", want: http.StatusUnauthorized},
		{name: "another origin", token: session.Token(), origin: "http://evil.test", want: http.StatusForbidden},
		{name: "another host", token: session.Token(), host: "evil.test", want: http.StatusForbidden},
		{name: "the session's own page", token: session.Token(), origin: srv.URL, want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/next", nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tt.token != "" {
				req.Header.Set("X-Goldset-Token", tt.token)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.host != "" {
				req.Host = tt.host
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.want)
			}
			if got := res.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: the token rides in the page URL", got)
			}
			if got := res.Header.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q, want no-referrer", got)
			}
		})
	}
}

// TestGoldSetUIIsUnrunnableInCI pins the environment check. It is safe to call the
// verb here only because that check precedes flag parsing and anything that could
// listen -- there is no path in an automated environment that reaches net.Listen.
func TestGoldSetUIIsUnrunnableInCI(t *testing.T) {
	t.Setenv("CI", "1")
	if code := runGoldSetUI(nil); code != 2 {
		t.Errorf("goldset-ui exited %d under CI, want 2: ground truth is written by a person who chose to write it (ADR-0043)", code)
	}
}

// uiSessionResp is /api/session's wire shape, decoded for the fields a re-fetching
// session has to state on screen.
type uiSessionResp struct {
	Authority string `json:"authority"`
	Refetch   bool   `json:"refetch"`
}

// session reads /api/session through the same front door the page does.
func (c *uiClient) session() uiSessionResp {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, "/api/session", nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET /api/session = %d: %s", status, raw)
	}
	var out uiSessionResp
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("decode /api/session: %v (%s)", err, raw)
	}
	return out
}

// newLiveTestGoldSet writes a gold set whose rows point at a local server, each
// carrying the same captured text the /same page serves back. Every row's Proposed
// Label is detail, so one keystroke answers it and the cursor advances without a note.
func newLiveTestGoldSet(t *testing.T, base string, paths ...string) (dir string, ids map[string]string) {
	t.Helper()
	dir = t.TempDir()
	rows := make([]goldRow, 0, len(paths))
	ids = map[string]string{}
	for i, path := range paths {
		url := base + path
		rows = append(rows, goldRow{
			URL: url, Verdict: true, TS: fmt.Sprintf("2026-08-01T09:00:%02dZ", i),
			Stratum: stratumRandom, Weight: 1, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{
				ProposedBy: "llm:SENTINEL-PROPOSER", ProposedAt: "2026-08-01T09:00:00Z",
				ProposedLabel: bench.ExtractDetail, Note: "SENTINEL-PROPOSER-NOTE",
			},
			Content: crawler.Content{
				Title:       goldRefetchTestTitle,
				MainContent: goldRefetchTestCaptured(),
				URLs:        []string{base + "/jobs/one"},
			},
		})
		ids[path] = rowID(url)
	}
	if err := writeGoldSetFiles(dir, rows); err != nil {
		t.Fatalf("writeGoldSetFiles: %v", err)
	}
	return dir, ids
}

// newLiveTestSession opens a session over a gold set whose rows point at a local
// server, measuring Capture Fidelity through the real refetcher.
func newLiveTestSession(t *testing.T, dir, base, cacheDir string) *goldSetUISession {
	t.Helper()
	session, err := newGoldSetUISession(goldUIConfig{
		Dir: dir, By: goldUITestConfirmer, FlushEvery: 10,
		JournalPath: filepath.Join(t.TempDir(), "journal.jsonl"),
		ScratchDir:  t.TempDir(),
		Refetch:     newGoldRefetchTestRefetcher(t, base, cacheDir, goldRefetchMaxAge),
	})
	if err != nil {
		t.Fatalf("newGoldSetUISession: %v", err)
	}
	t.Cleanup(func() { _ = session.journal.Close() })
	return session
}

// measuredRows counts the queue rows that carry a Capture Fidelity report, read under
// the session lock exactly as the handlers read it.
func measuredRows(s *goldSetUISession) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.queue {
		if q.fidelity != nil {
			n++
		}
	}
	return n
}

// TestGoldSetUIShowsFidelityOnEveryRow is the API seam this ticket adds: every row the
// tool serves states its Capture Fidelity, and a `gone` row is refused a live view at
// the wire, not merely in the page's CSS (ADR-0047).
func TestGoldSetUIShowsFidelityOnEveryRow(t *testing.T) {
	server := newGoldRefetchTestServer(t, "")
	dir, _ := newLiveTestGoldSet(t, server.URL, "/same", "/drifted", "/withdrawn")
	session := newLiveTestSession(t, dir, server.URL, t.TempDir())
	client := newUIClient(t, session)

	// Indexed by row id, never by queue position: the ids depend on the server's port,
	// and the queue is served in row-id order.
	want := map[string]struct {
		state    string
		liveView bool
	}{
		rowID(server.URL + "/same"):      {"same", true},
		rowID(server.URL + "/drifted"):   {"drifted", true},
		rowID(server.URL + "/withdrawn"): {"gone", false},
	}

	if info := client.session(); !info.Refetch {
		t.Error("/api/session reports refetch=false on a measuring session")
	} else if !strings.Contains(info.Authority, "captured content") || !strings.Contains(info.Authority, "aid") {
		t.Errorf("the authority line is %q, want it to name the captured content as the authority AND the re-fetch as an aid (ADR-0047)", info.Authority)
	}

	// The loop count is taken before the map is drained: `want` is what is still owed.
	for range len(want) {
		// The blind view is still blind: the fidelity field is an attribute of the
		// RE-FETCH, and it must not have made the wire carry anything that answers the
		// question being asked (ADR-0048).
		_, raw := client.do(http.MethodGet, "/api/next", nil)
		for _, leak := range []string{"SENTINEL-PROPOSER", "proposed", "stratum", "verdict", "label", "weight", "renderer", "expected"} {
			if bytes.Contains(bytes.ToLower(raw), []byte(strings.ToLower(leak))) {
				t.Errorf("a measured blind view carries %q: %s", leak, raw)
			}
		}
		view := client.ok(http.MethodGet, "/api/next", nil)
		if view.Question == nil {
			t.Fatalf("the queue ran out early: %+v", view)
		}
		expect, ok := want[view.Question.ID]
		if !ok {
			t.Fatalf("the tool served an unexpected row %s", view.Question.ID)
		}
		f := view.Question.Fidelity
		if f == nil {
			t.Fatalf("row %s carries no fidelity; every row states its Capture Fidelity (ADR-0047)", view.Question.ID)
		}
		if f.State != expect.state {
			t.Errorf("row %s measured %q at retention %.2f, want %q -- %s", view.Question.ID, f.State, f.Retention, expect.state, f.Reason)
		}
		if f.LiveView != expect.liveView {
			t.Errorf("row %s live_view = %v, want %v: a gone row is never offered a live view (ADR-0047)", view.Question.ID, f.LiveView, expect.liveView)
		}
		if f.Reason == "" || f.At == "" {
			t.Errorf("row %s carries reason %q at %q, want both stated", view.Question.ID, f.Reason, f.At)
		}
		delete(want, view.Question.ID)
		client.ok(http.MethodPost, "/api/answer", map[string]string{"id": view.Question.ID, "label": "detail"})
	}
	if len(want) != 0 {
		t.Errorf("%d rows were never served: %v", len(want), want)
	}
}

// TestGoldSetUINeverWritesTheRefetchOntoARow is ADR-0047's hardest rule as a test: the
// re-fetched bytes are a working artefact. Nothing the live page says may reach the
// record, and the row's captured content is what it always was.
func TestGoldSetUINeverWritesTheRefetchOntoARow(t *testing.T) {
	server := newGoldRefetchTestServer(t, "")
	dir, ids := newLiveTestGoldSet(t, server.URL, "/withdrawn", "/drifted")
	cacheDir := t.TempDir()
	before := recordOf(t, dir)

	session := newLiveTestSession(t, dir, server.URL, cacheDir)
	client := newUIClient(t, session)
	view := client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil || view.Question.Fidelity == nil {
		t.Fatalf("the first row was served unmeasured: %+v", view.Question)
	}
	client.ok(http.MethodPost, "/api/answer", map[string]string{"id": view.Question.ID, "label": "detail"})
	if flushed := client.ok(http.MethodPost, "/api/flush", struct{}{}).Flushed; flushed != 1 {
		t.Fatalf("the flush wrote %d decisions, want 1", flushed)
	}

	after := recordOf(t, dir)
	for id, was := range before {
		now, ok := after[id]
		if !ok {
			t.Fatalf("row %s vanished from the record", id)
		}
		if now.URL != was.URL || now.Verdict != was.Verdict || now.TS != was.TS ||
			now.Renderer != was.Renderer || now.Stratum != was.Stratum || now.Weight != was.Weight {
			t.Errorf("row %s: the re-fetch moved fields it may never touch: %+v, was %+v", id, now, was)
		}
		if !reflect.DeepEqual(now.Content, was.Content) {
			t.Errorf("row %s: the captured content changed; a row's stored content is what the tap captured, full stop (ADR-0047)\n got %+v\nwant %+v", id, now.Content, was.Content)
		}
		if now.LabelProvenance.ProposedBy != was.LabelProvenance.ProposedBy ||
			now.LabelProvenance.ProposedAt != was.LabelProvenance.ProposedAt ||
			now.LabelProvenance.ProposedLabel != was.LabelProvenance.ProposedLabel {
			t.Errorf("row %s: the proposer's record moved: %+v, was %+v", id, now.LabelProvenance, was.LabelProvenance)
		}
	}
	// The one legitimate change: the row the human decided now carries their signature.
	if got := after[view.Question.ID].LabelProvenance.ConfirmedBy; got != goldUITestConfirmer {
		t.Errorf("the decided row is confirmed by %q, want %q", got, goldUITestConfirmer)
	}
	for path, id := range ids {
		if id == view.Question.ID {
			continue
		}
		if got := after[id].LabelProvenance.ConfirmedBy; got != "" {
			t.Errorf("row %s (%s) gained a confirmer nobody gave it: %q", id, path, got)
		}
	}

	// Nothing the LIVE page said may appear anywhere under the gold-set directory.
	sentinels := []string{"This position has been filled", "Position filled", "z000"}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the gold-set directory: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, sentinel := range sentinels {
			if bytes.Contains(data, []byte(sentinel)) {
				t.Errorf("%s carries %q from the live page; the re-fetch is never written back onto a row (ADR-0047)", entry.Name(), sentinel)
			}
		}
	}

	// It did land where it belongs: outside the repository, in the cache.
	cached, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("read the re-fetch cache: %v", err)
	}
	if len(cached) == 0 {
		t.Error("the re-fetch cache is empty, so the assertion above proves nothing")
	}
}

// TestGoldSetUIWithoutARefetcherMeasuresNothing pins -refetch=false: no fetch, no
// fidelity, no live view -- and the screen still names the captured content as the
// authority. It is also what keeps every other test in this file network-free.
func TestGoldSetUIWithoutARefetcherMeasuresNothing(t *testing.T) {
	session, _, _ := newTestSession(t, 10)
	client := newUIClient(t, session)

	view := client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil {
		t.Fatalf("no row was served: %+v", view)
	}
	if view.Question.Fidelity != nil {
		t.Errorf("a session with no refetcher measured %+v; it must fetch nothing at all", view.Question.Fidelity)
	}
	info := client.session()
	if info.Refetch {
		t.Error("/api/session reports refetch=true with no refetcher wired")
	}
	if info.Authority != goldUIAuthority {
		t.Errorf("the authority line is %q, want the captured-content statement unchanged: %q", info.Authority, goldUIAuthority)
	}
}

// TestGoldSetUIWarmsAheadOfTheLabeller pins the cost and the benefit of the warm loop:
// the next goldUIWarmAhead rows are measured before the labeller reaches them, and NOT
// one row more -- a session stopped after ten rows must not have fetched a hundred
// pages.
func TestGoldSetUIWarmsAheadOfTheLabeller(t *testing.T) {
	server := newGoldRefetchTestServer(t, "")
	paths := []string{}
	for i := range goldUIWarmAhead + 3 {
		paths = append(paths, fmt.Sprintf("/same?row=%d", i))
	}
	dir, _ := newLiveTestGoldSet(t, server.URL, paths...)
	session := newLiveTestSession(t, dir, server.URL, t.TempDir())

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go session.WarmLoop(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for measuredRows(session) < goldUIWarmAhead && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := measuredRows(session); got != goldUIWarmAhead {
		t.Fatalf("the warm loop measured %d rows, want exactly %d ahead of the labeller", got, goldUIWarmAhead)
	}
	// Give it a moment to run past its bound if it were going to.
	time.Sleep(50 * time.Millisecond)
	if got := measuredRows(session); got != goldUIWarmAhead {
		t.Errorf("the warm loop measured %d rows with the cursor still on the first, want it bounded at %d", got, goldUIWarmAhead)
	}
	if got := server.distinctPages(); got != goldUIWarmAhead {
		t.Errorf("the warm loop fetched %d distinct pages, want %d", got, goldUIWarmAhead)
	}

	// The window is positional: one decision admits exactly ONE further row, so the
	// aid costs one fetch per row read plus a fixed lookahead -- not five per row.
	client := newUIClient(t, session)
	view := client.ok(http.MethodGet, "/api/next", nil)
	if view.Question == nil {
		t.Fatalf("no row was served: %+v", view)
	}
	client.ok(http.MethodPost, "/api/answer", map[string]string{"id": view.Question.ID, "label": "detail"})

	deadline = time.Now().Add(10 * time.Second)
	for measuredRows(session) < goldUIWarmAhead+1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := measuredRows(session); got != goldUIWarmAhead+1 {
		t.Errorf("one decision left %d rows measured, want %d: the window is positional, so advancing one row admits one more",
			got, goldUIWarmAhead+1)
	}
}
