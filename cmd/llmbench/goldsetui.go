// This file is llmbench's blind labelling session (ADR-0048, #286): the queue of
// Extract Gold Set rows awaiting a human confirmer, the decisions taken over them,
// the journal that makes a decision survive a crash, and the synthesis of the
// goldset-apply arguments a batch of decisions becomes. It writes the record through
// that verb and nothing else -- goldset-apply is the only writer (ADR-0048) -- and it
// touches no network and no model. The verb, its loopback listener and its handlers
// live beside this in goldsetuiverb.go.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

const (
	// goldUIDefaultAddr is the loopback address the tool serves on. A fixed port
	// rather than a random one so a labeller's bookmark keeps working across sittings.
	goldUIDefaultAddr = "localhost:7777"
	// goldUIDefaultFlushEvery is how many decisions buffer before the record is
	// rewritten. The substrate is 6.4 MB, so a write per keystroke would rewrite it
	// forty times a sitting; the journal is what makes buffering safe (#283).
	goldUIDefaultFlushEvery = 10
	// goldUINoteRunes is the hard ceiling on a note, in RUNES. goldProvenance asks
	// for <= 80 characters as a convention; this is the ceiling that keeps one note on
	// one line of labels.tsv, and the page shows a live count against 80 so the
	// convention stays visible where the note is written.
	goldUINoteRunes = 120
	// goldUITokenBytes is the session token's entropy. It is the only thing standing
	// between a page the labeller happens to have open and the committed record.
	goldUITokenBytes = 32
)

// goldUIConfig groups a labelling session's dependencies and settings.
type goldUIConfig struct {
	// Dir is the gold-set directory the decisions land in.
	Dir string
	// By is the confirmer's HUMAN name, validated by the caller (machineName).
	By string
	// Stratum restricts the pass to one sampling cell; empty serves every row still
	// owed a confirmer.
	Stratum goldStratum
	// FlushEvery is how many buffered decisions trigger a write. Always >= 1.
	FlushEvery int
	// JournalPath is the append-only session journal, a working artifact outside the
	// repository and never the record.
	JournalPath string
	// ScratchDir is where the synthesized goldset-apply argument files are written.
	// They are kept after a batch as the readable account of what it applied.
	ScratchDir string
}

// goldUIQueueRow is one row awaiting a decision, with the session's memory of what
// the labeller has already been shown about it.
type goldUIQueueRow struct {
	row goldRow
	// revealed records that this labeller has already seen this row's Proposed
	// Label. It survives an undo: blindness, once spent, is spent, and the screen says
	// so rather than pretending otherwise.
	revealed bool
	// decision is the decision currently held for this row, nil until one is taken
	// and nil again after an undo.
	decision *goldUIDecision
	// written reports that the decision currently held reached the record. A new
	// decision replacing it starts unwritten again, which is what keeps undo able to
	// tell a buffered decision (retractable in memory) from a signed one (changed only
	// by relabelling).
	written bool
}

// goldUIDecision is one labelling decision: the row, the label the labeller chose,
// the note they wrote, and whether it agreed with the Proposed Label.
type goldUIDecision struct {
	ID    string             `json:"id"`
	URL   string             `json:"url"`
	Label bench.ExtractLabel `json:"label"`
	Note  string             `json:"note"`
	// Relabel is true when Label differs from the label ON THE RECORD, which is what
	// sends the decision down goldset-apply's proposals path. It is deliberately NOT
	// the same comparison as Agreed: Agreed is measured against the PROPOSED label
	// (ADR-0048's measurement), and the two differ on a row whose label was overridden
	// by an earlier pass that never confirmed it.
	Relabel bool `json:"relabel"`
	// Agreed reports that Label is the Proposed Label. Comparable reports that the row
	// carried a Proposed Label at all -- a row with none is neither agreement nor
	// disagreement, exactly as labelAgreement counts it, so the running rate on screen
	// is the same number goldset-apply prints.
	Agreed     bool   `json:"agreed"`
	Comparable bool   `json:"comparable"`
	At         string `json:"at"` // RFC3339 UTC, the instant the decision completed
}

// goldSetUISession is one labelling sitting: the queue, the cursor over it, the
// decisions buffered for the next write, and the journal behind them.
type goldSetUISession struct {
	cfg   goldUIConfig
	token string

	mu      sync.Mutex
	queue   []goldUIQueueRow
	pos     int
	pending *goldUIDecision  // answered, awaiting the note its answer requires
	buffer  []goldUIDecision // decisions taken and not yet written to the record
	flushed int
	batches int
	journal *os.File
}

// newGoldSetUISession reads the gold set at cfg.Dir, draws the queue of rows still
// owed a human confirmer, opens the journal and mints the session token. The queue is
// drawn ONCE: a flush stamps confirmations on rows already behind the cursor, so
// redrawing it would only re-derive the same list. Resuming across sittings is the
// same mechanism -- the next session's draw skips whatever this one confirmed.
func newGoldSetUISession(cfg goldUIConfig) (*goldSetUISession, error) {
	if strings.TrimSpace(cfg.By) == "" {
		return nil, fmt.Errorf("a confirmation is somebody's signature and never a default (ADR-0043)")
	}
	if machineName(cfg.By) {
		return nil, fmt.Errorf("%q is not a human; confirmation cannot be automated (ADR-0043)", cfg.By)
	}
	if cfg.Stratum != "" && !cfg.Stratum.Valid() {
		return nil, fmt.Errorf("%q is not a known stratum", cfg.Stratum)
	}
	if cfg.FlushEvery < 1 {
		return nil, fmt.Errorf("flush-every must be >= 1, got %d", cfg.FlushEvery)
	}

	substrate, _ := goldSetPaths(cfg.Dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		return nil, err
	}

	queue := []goldUIQueueRow{}
	for _, row := range rows {
		if row.LabelProvenance.ConfirmedBy != "" {
			continue
		}
		if cfg.Stratum != "" && row.Stratum != cfg.Stratum {
			continue
		}
		// The same backfill applyLabels runs before its merge (ADR-0048), done here in
		// memory only so the reveal names the label the write path will preserve. A row
		// with no confirmer still carries the label its proposer put on it, so its
		// current label IS the Proposed Label.
		prov := &row.LabelProvenance
		if prov.ProposedLabel == "" && prov.ProposedBy != "" && row.Label.Valid() {
			prov.ProposedLabel = row.Label
		}
		queue = append(queue, goldUIQueueRow{row: row})
	}
	// Row-id order for the reason renderConfirmSheet already gives: a sha256 of the
	// URL is stable, quotable and INDEPENDENT of the label, so the order can never
	// group the rows by the answer being checked.
	sort.Slice(queue, func(i, j int) bool { return rowID(queue[i].row.URL) < rowID(queue[j].row.URL) })

	raw := make([]byte, goldUITokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("mint the session token: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.JournalPath), 0o755); err != nil {
		return nil, fmt.Errorf("create the journal directory: %w", err)
	}
	journal, err := os.OpenFile(cfg.JournalPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open the journal: %w", err)
	}

	s := &goldSetUISession{cfg: cfg, token: hex.EncodeToString(raw), queue: queue, journal: journal}
	if err := s.journalLocked(goldUIJournalEntry{
		Event: "session", At: goldUINow(), By: cfg.By, Dir: cfg.Dir, Stratum: cfg.Stratum, Queue: len(queue),
	}); err != nil {
		_ = journal.Close()
		return nil, err
	}
	return s, nil
}

// goldUINow is the instant a journal entry or a batch is stamped with, in the same
// RFC3339 UTC shape goldset-apply writes into the record.
func goldUINow() string { return time.Now().UTC().Format(time.RFC3339) }

// goldUIJournalEntry is one line of the session journal: an append-only, fsync'd
// record of every decision the moment it is taken, so a crash between flushes costs a
// rewrite of the batch and not the labeller's reading (ADR-0048). It is a working
// artifact outside the repository, never the record.
type goldUIJournalEntry struct {
	Event   string             `json:"event"` // "session" | "decision" | "undo" | "flush" | "flush-failed"
	At      string             `json:"at"`
	By      string             `json:"by,omitempty"`
	Dir     string             `json:"dir,omitempty"`
	Stratum goldStratum        `json:"stratum,omitempty"`
	Queue   int                `json:"queue,omitempty"`
	ID      string             `json:"id,omitempty"`
	URL     string             `json:"url,omitempty"`
	Label   bench.ExtractLabel `json:"label,omitempty"`
	Note    string             `json:"note,omitempty"`
	Agreed  *bool              `json:"agreed,omitempty"`
	Outcome string             `json:"outcome,omitempty"`
	Count   int                `json:"count,omitempty"` // flush: decisions written
	Err     string             `json:"err,omitempty"`
}

// journalLocked appends one entry and fsyncs it. "The moment it is taken" means
// durable, not buffered: a decision that cannot be journalled must not enter the
// buffer, so the error is returned to the caller and aborts the decision.
func (s *goldSetUISession) journalLocked(entry goldUIJournalEntry) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entry); err != nil {
		return fmt.Errorf("encode the journal entry: %w", err)
	}
	if _, err := s.journal.WriteString(b.String()); err != nil {
		return fmt.Errorf("write the journal: %w", err)
	}
	if err := s.journal.Sync(); err != nil {
		return fmt.Errorf("sync the journal: %w", err)
	}
	return nil
}

// goldUINoteRequired reports whether a decision must carry a note. ADR-0048: a note
// is required whenever the human disagrees with the Proposed Label -- those rows are
// where the proposer is actually wrong, and the note is what a prompt fix is built
// from -- and on ambiguous, where it records what the tension was. A row carrying no
// Proposed Label is treated as disagreement: there is no proposer's argument for the
// record to fall back on. On agreement the note stays EMPTY so the proposer's own note
// survives.
func goldUINoteRequired(row goldRow, chosen bench.ExtractLabel) bool {
	agreed, comparable := goldUIAgreed(row, chosen)
	return !comparable || !agreed || chosen == bench.ExtractAmbiguous
}

// goldUIAgreed reports whether chosen agrees with the row's Proposed Label, and
// whether the two are comparable at all -- a row carrying no Proposed Label is neither
// agreement nor disagreement, exactly as labelAgreement counts it.
func goldUIAgreed(row goldRow, chosen bench.ExtractLabel) (agreed, comparable bool) {
	proposed := row.LabelProvenance.ProposedLabel
	if proposed == "" {
		return false, false
	}
	return chosen == proposed, true
}

// goldUIDedupe returns the decisions keyed by row id with the LAST decision winning,
// sorted by id. A row decided twice in one batch is one row on the record, and the
// batch files are read by a human, so they are ordered rather than arrival-shaped.
func goldUIDedupe(decisions []goldUIDecision) []goldUIDecision {
	byID := map[string]goldUIDecision{}
	for _, d := range decisions {
		byID[d.ID] = d
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]goldUIDecision, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

// goldUIProposalLines renders the id<TAB>label<TAB>note file goldset-apply's
// -proposals path reads, for exactly the decisions that change a label or carry a
// note. An agreement emits NO line: the row's label and the proposer's note must be
// left alone. It returns nil when no decision needs one, which is what tells the
// caller to omit -proposals entirely.
func goldUIProposalLines(by, stamp string, decisions []goldUIDecision) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# proposals synthesized by goldset-ui for %s at %s\n", flattenField(by), stamp)
	b.WriteString("# id\tlabel\tnote -- one line per decision that changes a label or carries a note\n")
	lines := 0
	for _, d := range goldUIDedupe(decisions) {
		if !d.Relabel && d.Note == "" {
			continue
		}
		lines++
		// Every note is flattened before it reaches a TSV: a tab or a newline in a note
		// would otherwise split a labels.tsv line.
		if note := flattenField(d.Note); note != "" {
			fmt.Fprintf(&b, "%s\t%s\t%s\n", d.ID, d.Label, note)
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\n", d.ID, d.Label)
	}
	if lines == 0 {
		return nil
	}
	return []byte(b.String())
}

// goldUIConfirmIDLines renders the -confirm-ids file: every decision in the batch,
// one bare row id per line, sorted, with a header comment so the scratch file is a
// readable account of what the batch signed off.
func goldUIConfirmIDLines(by, stamp string, decisions []goldUIDecision) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# confirmations synthesized by goldset-ui for %s at %s\n", flattenField(by), stamp)
	for _, d := range goldUIDedupe(decisions) {
		b.WriteString(d.ID)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// goldUIApplyArgs synthesizes the goldset-apply argument list for one batch. It is
// the whole of this tool's write path (ADR-0048): the relabels travel the proposals
// path and the confirmations the id list, in ONE invocation, because the id list is
// validated against the MERGED rows and so a label proposed in the same run counts.
//
// Values use the -flag=value form so a confirmer's name can never be mistaken for a
// flag. -proposed-by is DELIBERATELY never passed: it would stamp the labeller as the
// proposer of their own label, which applyLabels would then record as a Proposed Label
// equal to it -- a confirmation agreeing with itself, inflating exactly the number
// this pass exists to measure. -confirm-stratum is never passed either: it is mutually
// exclusive with the id list, and the id list is what makes the batch exact.
func goldUIApplyArgs(dir, by, proposalsPath, confirmIDsPath string) []string {
	args := []string{"-dir=" + dir}
	if proposalsPath != "" {
		args = append(args, "-proposals="+proposalsPath)
	}
	return append(args, "-confirmed-by="+by, "-confirm-ids="+confirmIDsPath)
}

// goldUIQuestion is the blind view: EXACTLY what the labeller may see before
// answering. The Proposed Label, the live extractor's verdict, the stratum and every
// structured-data-derived field are absent from this struct, so they cannot be read
// off the wire by a page that asked nicely (ADR-0048).
type goldUIQuestion struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Head  string `json:"head"`
	Mid   string `json:"mid"`

	URLsTotal    int `json:"urls_total"`
	URLsSameHost int `json:"urls_same_host"`
	URLsJoblike  int `json:"urls_joblike"`
}

// goldUIQuestionOf builds a row's blind view from confirmViewOf, so the labelling
// tool and the confirmation sheet can never disagree about what a page says, and then
// copies field by field. The copy is deliberate: embedding confirmView would let a
// field added to that struct -- it already carries the proposed label and its proposer
// -- leak into the blind view unnoticed.
func goldUIQuestionOf(row goldRow) goldUIQuestion {
	c := confirmViewOf(row)
	return goldUIQuestion{
		ID:           c.ID,
		URL:          c.URL,
		Title:        c.Title,
		Head:         c.Head,
		Mid:          c.Mid,
		URLsTotal:    c.URLsTotal,
		URLsSameHost: c.URLsSameHost,
		URLsJoblike:  c.URLsJoblike,
	}
}

// goldUIReveal is what the labeller is shown only AFTER answering: what the proposer
// said, whether they agreed with it, and the fields withheld until the answer was in.
// Chosen is empty on a row whose blindness was spent by an answer since undone, which
// the screen says rather than pretending the row is still blind.
type goldUIReveal struct {
	ProposedLabel bench.ExtractLabel `json:"proposed_label"`
	ProposedBy    string             `json:"proposed_by"`
	ProposerNote  string             `json:"proposer_note"`
	CurrentLabel  bench.ExtractLabel `json:"current_label"`
	Stratum       goldStratum        `json:"stratum"`
	Verdict       bool               `json:"verdict"`
	Chosen        bench.ExtractLabel `json:"chosen"`
	Agreed        bool               `json:"agreed"`
	Comparable    bool               `json:"comparable"`
	NoteRequired  bool               `json:"note_required"`
}

// goldUIProgress is the session counter: where the labeller is, what the sitting has
// cost, and the running agreement rate -- the number ADR-0048 asks a blind pass to
// produce, put where a systematic disagreement becomes obvious while there is still
// time to act on it.
type goldUIProgress struct {
	Position   int `json:"position"` // 1-based, 0 when the queue is exhausted
	Total      int `json:"total"`
	Decided    int `json:"decided"`
	Buffered   int `json:"buffered"`
	Flushed    int `json:"flushed"`
	Agreed     int `json:"agreed"`
	Comparable int `json:"comparable"`
}

// goldUIPending is the answer a labeller has given on the row still on screen, held
// back from the buffer until the note it requires is in.
type goldUIPending struct {
	Label bench.ExtractLabel `json:"label"`
}

// goldUIView is one screen: the row being asked about, the progress behind it, and --
// only once the labeller has answered -- the reveal.
type goldUIView struct {
	Done     bool            `json:"done"`
	Progress goldUIProgress  `json:"progress"`
	Question *goldUIQuestion `json:"question"`
	Reveal   *goldUIReveal   `json:"reveal"`
	Pending  *goldUIPending  `json:"pending"`
}

// goldUIResult is a view plus what the request just did to the record.
type goldUIResult struct {
	goldUIView
	// Recorded reports that the decision is complete and in the buffer. It is false
	// on an answer still owing its note: a decision without its required note is not
	// yet a decision.
	Recorded bool   `json:"recorded"`
	Flushed  int    `json:"flushed"` // decisions written to the record by THIS request
	Outcome  string `json:"outcome,omitempty"`
	ID       string `json:"id,omitempty"`
}

// Token is the session key the page must present on every request.
func (s *goldSetUISession) Token() string { return s.token }

// Queue is the number of rows this sitting was drawn to confirm.
func (s *goldSetUISession) Queue() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Current returns the row on screen, its progress, and -- only if this labeller has
// already answered it -- its reveal.
func (s *goldSetUISession) Current() goldUIView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentViewLocked()
}

func (s *goldSetUISession) currentViewLocked() goldUIView {
	if s.pos >= len(s.queue) {
		return goldUIView{Done: true, Progress: s.progressLocked()}
	}
	return s.viewLocked(s.pos)
}

// viewLocked builds the screen for queue index i.
func (s *goldSetUISession) viewLocked(i int) goldUIView {
	q := s.queue[i]
	question := goldUIQuestionOf(q.row)
	view := goldUIView{Progress: s.progressLocked(), Question: &question}
	if !q.revealed {
		return view
	}
	reveal := goldUIReveal{
		ProposedLabel: q.row.LabelProvenance.ProposedLabel,
		ProposedBy:    q.row.LabelProvenance.ProposedBy,
		ProposerNote:  q.row.LabelProvenance.Note,
		CurrentLabel:  q.row.Label,
		Stratum:       q.row.Stratum,
		Verdict:       q.row.Verdict,
	}
	switch {
	case s.pending != nil && s.pending.ID == question.ID:
		reveal.Chosen, reveal.Agreed, reveal.Comparable = s.pending.Label, s.pending.Agreed, s.pending.Comparable
		reveal.NoteRequired = true
		view.Pending = &goldUIPending{Label: s.pending.Label}
	case q.decision != nil:
		reveal.Chosen, reveal.Agreed, reveal.Comparable = q.decision.Label, q.decision.Agreed, q.decision.Comparable
	}
	view.Reveal = &reveal
	return view
}

func (s *goldSetUISession) progressLocked() goldUIProgress {
	p := goldUIProgress{Total: len(s.queue), Buffered: len(s.buffer), Flushed: s.flushed}
	if s.pos < len(s.queue) {
		p.Position = s.pos + 1
	}
	for _, q := range s.queue {
		if q.decision == nil {
			continue
		}
		p.Decided++
		if !q.decision.Comparable {
			continue
		}
		p.Comparable++
		if q.decision.Agreed {
			p.Agreed++
		}
	}
	return p
}

// Answer records the labeller's verdict on id and reveals the Proposed Label. When
// the reveal requires a note the decision is held PENDING rather than buffered:
// whether a note is owed can only be known after the reveal, which is the whole shape
// of a blind confirmation, so the answer and the note are two steps and a decision is
// complete only when both are in.
func (s *goldSetUISession) Answer(id string, label bench.ExtractLabel) (goldUIResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pos >= len(s.queue) {
		return goldUIResult{}, goldUIConflict("the queue is exhausted; there is nothing left to answer")
	}
	i := s.pos
	if got := rowID(s.queue[i].row.URL); got != id {
		return goldUIResult{}, goldUIConflict(fmt.Sprintf("%q is not the row on screen (%s); reload before answering", id, got))
	}
	if !label.Valid() {
		return goldUIResult{}, goldUIBadRequest(fmt.Sprintf("%q is not one of the four labels", label))
	}
	if s.pending != nil {
		return goldUIResult{}, goldUIConflict("this row is already answered and owes a note; undo it to answer again")
	}

	row := s.queue[i].row
	agreed, comparable := goldUIAgreed(row, label)
	decision := goldUIDecision{
		ID: id, URL: row.URL, Label: label,
		Relabel:    label != row.Label,
		Agreed:     agreed,
		Comparable: comparable,
		At:         goldUINow(),
	}
	// Blindness is spent by the answer itself, before the reveal is built: an undo
	// must not restore it.
	s.queue[i].revealed = true

	if goldUINoteRequired(row, label) {
		s.pending = &decision
		return goldUIResult{goldUIView: s.viewLocked(i)}, nil
	}
	written, err := s.completeLocked(i, decision)
	if err != nil {
		return goldUIResult{}, err
	}
	return goldUIResult{goldUIView: s.viewLocked(i), Recorded: true, Flushed: written}, nil
}

// Note completes the pending decision. It is refused when no decision is pending --
// a note on an agreement would overwrite the proposer's own -- when the note is empty
// after flattening, and when it exceeds goldUINoteRunes.
func (s *goldSetUISession) Note(id, note string) (goldUIResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending == nil {
		return goldUIResult{}, goldUIBadRequest("no decision is awaiting a note; a note on an agreement would overwrite the proposer's own")
	}
	if s.pending.ID != id {
		return goldUIResult{}, goldUIConflict(fmt.Sprintf("%q is not the row awaiting a note (%s)", id, s.pending.ID))
	}
	flat := flattenField(note)
	if flat == "" {
		return goldUIResult{}, goldUIBadRequest("a note is required here and cannot be blank")
	}
	if n := len([]rune(flat)); n > goldUINoteRunes {
		return goldUIResult{}, goldUIBadRequest(fmt.Sprintf("the note is %d characters; the ceiling is %d", n, goldUINoteRunes))
	}

	i := s.pos
	decision := *s.pending
	decision.Note = flat
	decision.At = goldUINow()
	s.pending = nil
	written, err := s.completeLocked(i, decision)
	if err != nil {
		return goldUIResult{}, err
	}
	return goldUIResult{goldUIView: s.viewLocked(i), Recorded: true, Flushed: written}, nil
}

// completeLocked journals a finished decision, buffers it, advances the cursor and
// writes the batch when it has reached its size. It returns how many decisions this
// call wrote to the record.
func (s *goldSetUISession) completeLocked(i int, decision goldUIDecision) (int, error) {
	agreed := decision.Agreed
	if err := s.journalLocked(goldUIJournalEntry{
		Event: "decision", At: decision.At, ID: decision.ID, URL: decision.URL,
		Label: decision.Label, Note: decision.Note, Agreed: &agreed,
	}); err != nil {
		return 0, err
	}
	s.queue[i].decision = &decision
	s.queue[i].written = false
	s.buffer = append(s.buffer, decision)
	s.pos = i + 1
	if len(s.buffer) < s.cfg.FlushEvery {
		return 0, nil
	}
	return s.flushLocked()
}

// Undo reverses the last unflushed decision. A decision still in the buffer is
// removed and its row re-served; an answer awaiting its note is discarded; a decision
// already WRITTEN is not undone -- it is a signed act -- and its row is re-served for
// RELABELLING instead, which goldset-apply records by retracting the confirmation
// whose label changed and re-stamping it in the same invocation.
func (s *goldSetUISession) Undo() (goldUIResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending != nil {
		id := s.pending.ID
		s.pending = nil
		if err := s.journalLocked(goldUIJournalEntry{Event: "undo", At: goldUINow(), ID: id, Outcome: "discarded"}); err != nil {
			return goldUIResult{}, err
		}
		return goldUIResult{goldUIView: s.currentViewLocked(), Outcome: "discarded", ID: id}, nil
	}
	prev := s.pos - 1
	if prev < 0 || s.queue[prev].decision == nil {
		return goldUIResult{goldUIView: s.currentViewLocked(), Outcome: "nothing"}, nil
	}

	id := s.queue[prev].decision.ID
	outcome := "undone"
	if s.queue[prev].written {
		// A written decision is a signature on the record. It is not erased here; the
		// row is re-served so a new label can retract and replace it through the one
		// writer (ADR-0048).
		outcome = "relabel"
	} else {
		s.buffer = goldUIWithoutLast(s.buffer, id)
	}
	s.queue[prev].decision = nil
	s.queue[prev].written = false
	s.pos = prev
	if err := s.journalLocked(goldUIJournalEntry{Event: "undo", At: goldUINow(), ID: id, Outcome: outcome}); err != nil {
		return goldUIResult{}, err
	}
	return goldUIResult{goldUIView: s.currentViewLocked(), Outcome: outcome, ID: id}, nil
}

// goldUIWithoutLast drops the LAST decision for id, which is the one an undo takes
// back. Earlier decisions for the same id in the same batch stay: they were taken,
// and the batch file dedupes them.
func goldUIWithoutLast(buffer []goldUIDecision, id string) []goldUIDecision {
	for i := len(buffer) - 1; i >= 0; i-- {
		if buffer[i].ID == id {
			return append(buffer[:i:i], buffer[i+1:]...)
		}
	}
	return buffer
}

// Flush writes the buffered decisions to the record through goldset-apply and
// nothing else (ADR-0048).
func (s *goldSetUISession) Flush() (goldUIResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	written, err := s.flushLocked()
	if err != nil {
		return goldUIResult{}, err
	}
	return goldUIResult{goldUIView: s.currentViewLocked(), Flushed: written}, nil
}

// flushLocked synthesizes the batch files, runs goldset-apply over them and clears
// the buffer ONLY on success: a decision the verb refused is still owed, and the
// journal still holds it.
func (s *goldSetUISession) flushLocked() (int, error) {
	if len(s.buffer) == 0 {
		return 0, nil
	}
	stamp := goldUINow()
	s.batches++
	batch := s.batches

	confirmPath := filepath.Join(s.cfg.ScratchDir, fmt.Sprintf("confirmed-%02d.txt", batch))
	if err := os.WriteFile(confirmPath, goldUIConfirmIDLines(s.cfg.By, stamp, s.buffer), 0o644); err != nil {
		return 0, fmt.Errorf("write the batch confirmation list: %w", err)
	}
	proposalsPath := ""
	if lines := goldUIProposalLines(s.cfg.By, stamp, s.buffer); lines != nil {
		proposalsPath = filepath.Join(s.cfg.ScratchDir, fmt.Sprintf("proposals-%02d.tsv", batch))
		if err := os.WriteFile(proposalsPath, lines, 0o644); err != nil {
			return 0, fmt.Errorf("write the batch proposals: %w", err)
		}
	}

	if code := runGoldSetApply(goldUIApplyArgs(s.cfg.Dir, s.cfg.By, proposalsPath, confirmPath)); code != 0 {
		err := fmt.Errorf("goldset-apply refused batch %02d (exit %d); the record was not written and the batch is still buffered -- read the terminal for what it refused", batch, code)
		if jerr := s.journalLocked(goldUIJournalEntry{Event: "flush-failed", At: stamp, Count: len(s.buffer), Err: err.Error()}); jerr != nil {
			return 0, jerr
		}
		return 0, err
	}

	written := len(s.buffer)
	// The in-memory rows follow the record so a later decision on the same row -- a
	// relabel after an undo -- compares against what is actually written rather than
	// against what the file said at startup.
	for i := range s.queue {
		q := &s.queue[i]
		if q.decision == nil || q.written {
			continue
		}
		q.row.Label = q.decision.Label
		if q.decision.Note != "" {
			q.row.LabelProvenance.Note = q.decision.Note
		}
		q.row.LabelProvenance.ConfirmedBy = s.cfg.By
		q.written = true
	}
	s.buffer = nil
	s.flushed += written
	if err := s.journalLocked(goldUIJournalEntry{Event: "flush", At: stamp, Count: written}); err != nil {
		return written, err
	}
	return written, nil
}

// Close writes whatever the sitting still holds and closes the journal. A decision
// taken is a decision owed: shutting down is not a reason to drop one.
func (s *goldSetUISession) Close() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	written, err := s.flushLocked()
	if cerr := s.journal.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return written, err
}

// Summary is the closing account of a sitting: decisions taken, decisions written,
// and the agreement between this labeller and the proposers they read.
func (s *goldSetUISession) Summary() goldUIProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progressLocked()
}
