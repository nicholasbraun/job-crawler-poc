package bench

import "math"

// GateOutcome is the pre-LLM gate's verdict on one fixture: the three paths
// pagegate.CareerPage's (accept, certain) pair collapses to.
type GateOutcome int

const (
	GateReject        GateOutcome = iota // accept=false
	GateCertainAccept                    // accept && certain  -> skips the LLM
	GateUncertain                        // accept && !certain -> the LLM would confirm
)

// String renders a GateOutcome for the scorecard and diagnostics.
func (o GateOutcome) String() string {
	switch o {
	case GateReject:
		return "reject"
	case GateCertainAccept:
		return "certain-accept"
	case GateUncertain:
		return "uncertain"
	default:
		return "unknown"
	}
}

// GateOutcomeFrom maps pagegate.CareerPage's (accept, certain) pair to a
// GateOutcome. accept=false is a reject regardless of certain.
func GateOutcomeFrom(accept, certain bool) GateOutcome {
	if !accept {
		return GateReject
	}
	if certain {
		return GateCertainAccept
	}
	return GateUncertain
}

// Label is the human-owned binary ground truth for a fixture.
type Label string

const (
	LabelCareerPage    Label = "career_page"
	LabelNotCareerPage Label = "not_career_page"
)

// Valid reports whether l is one of the two known labels.
func (l Label) Valid() bool {
	return l == LabelCareerPage || l == LabelNotCareerPage
}

// Positive reports whether l is the positive class (a Career Page).
func (l Label) Positive() bool { return l == LabelCareerPage }

// Category slices the six Gold-Set strata. Scoring uses the binary Label; the
// Category only slices the report and pins the two structurally-fixed gate
// expectations (hub_ats_root, aggregator).
type Category string

const (
	CategoryHubATSRoot       Category = "hub_ats_root"       // +, gate-certain
	CategoryHubSelfHosted    Category = "hub_self_hosted"    // +, LLM
	CategoryJobPostingSingle Category = "job_posting_single" // -, dangerous FP
	CategoryCultureAbout     Category = "culture_about"      // -, LLM trap
	CategoryAggregator       Category = "aggregator"         // -, gate-certain reject
	CategoryUnrelated        Category = "unrelated"          // -
)

// Valid reports whether c is one of the six known categories.
func (c Category) Valid() bool {
	switch c {
	case CategoryHubATSRoot, CategoryHubSelfHosted, CategoryJobPostingSingle,
		CategoryCultureAbout, CategoryAggregator, CategoryUnrelated:
		return true
	default:
		return false
	}
}

// Positive reports whether c is a positive-class category (a real Career Page
// hub). It must agree with the fixture's Label; LoadManifest enforces that.
func (c Category) Positive() bool {
	return c == CategoryHubATSRoot || c == CategoryHubSelfHosted
}

// VerdictRow is one fixture's full pipeline outcome and the SOLE input to Score.
// Later tickets ADD fields (LLM verdict, repeat votes) -- never reshape these.
type VerdictRow struct {
	URL      string
	Category Category
	Label    Label
	Gate     GateOutcome
	// LLMConfirm is the classifier's Confirm verdict for this fixture. It is
	// meaningful ONLY when Gate == GateUncertain (the forwarded subset); for
	// reject / certain-accept rows it is unused and left false.
	LLMConfirm bool
}

// GateScorecard is the deterministic Gate regression report.
type GateScorecard struct {
	Total         int
	LLMCalls      int         // rows with GateUncertain (the gate forwards to the LLM)
	LLMCallRate   float64     // ratio(LLMCalls, Total), 4dp, 0 when Total==0
	Leaks         []string    // URLs: Label positive AND Gate==GateReject
	FalseCertains []string    // URLs: Label negative AND Gate==GateCertainAccept
	Violations    []Violation // per-category structural expectation failures
}

// Violation is a category whose gate verdict is structurally fixed but wrong: an
// ATS board root that does not certain-accept, or an aggregator that does not
// reject.
type Violation struct {
	URL      string
	Category Category
	Want     GateOutcome
	Got      GateOutcome
}

// ClassScore is a binary confusion matrix plus its derived rates. Precision,
// Recall, Accuracy, and F1 are rounded to 4dp and are 0 on a zero denominator.
type ClassScore struct {
	TP, FP, FN, TN int
	Total          int
	Precision      float64 // tp/(tp+fp)
	Recall         float64 // tp/(tp+fn)
	Accuracy       float64 // (tp+tn)/total
	F1             float64 // harmonic mean of Precision and Recall
}

// scoreClass folds a confusion matrix into a ClassScore. F1 is computed from the
// already-rounded Precision and Recall so it agrees with the printed rates.
func scoreClass(tp, fp, fn, tn int) ClassScore {
	total := tp + fp + fn + tn
	p := ratio(tp, tp+fp)
	r := ratio(tp, tp+fn)
	return ClassScore{
		TP: tp, FP: fp, FN: fn, TN: tn, Total: total,
		Precision: p,
		Recall:    r,
		Accuracy:  ratio(tp+tn, total),
		F1:        f1(p, r),
	}
}

// f1 is the harmonic mean of precision and recall, rounded to 4dp, or 0 when
// both are 0 (guards divide-by-zero).
func f1(precision, recall float64) float64 {
	if precision+recall == 0 {
		return 0
	}
	return math.Round(2*precision*recall/(precision+recall)*10000) / 10000
}

// LLMScorecard measures the classifier over the subset the gate forwarded
// (GateUncertain rows). Descriptive only -- it never affects Report.Failed().
type LLMScorecard struct {
	ClassScore
}

// EndToEndScorecard measures the production decision (reject=>negative,
// certain-accept=>positive, uncertain=>the LLM's Confirm verdict) against the
// gold label, overall and sliced by category.
type EndToEndScorecard struct {
	Overall    ClassScore
	ByCategory map[Category]ClassScore
}

// AllCategories is the fixed print order for per-category breakdowns.
var AllCategories = []Category{
	CategoryHubATSRoot, CategoryHubSelfHosted, CategoryJobPostingSingle,
	CategoryCultureAbout, CategoryAggregator, CategoryUnrelated,
}

// endToEndPositive is the production accept decision for one row: reject is
// negative, certain-accept is positive, uncertain defers to the LLM verdict.
func endToEndPositive(row VerdictRow) bool {
	switch row.Gate {
	case GateCertainAccept:
		return true
	case GateUncertain:
		return row.LLMConfirm
	default: // GateReject
		return false
	}
}

// Report is the full bench output. #48 fills only Gate; later tickets add
// LLM/EndToEnd/ReviewQueue fields alongside it.
type Report struct {
	Gate     GateScorecard
	LLM      LLMScorecard
	EndToEnd EndToEndScorecard
}

// Failed reports whether the run must exit non-zero: any Leak, False-Certain, or
// Violation. LLM numbers are descriptive and never move this.
func (r Report) Failed() bool {
	g := r.Gate
	return len(g.Leaks) > 0 || len(g.FalseCertains) > 0 || len(g.Violations) > 0
}

// Score folds verdict rows into the Report. PURE -- no parser, network, or LLM.
// The Leak, False-Certain, and Violation lists are three independent checks and
// preserve input order; a single row can appear in more than one (e.g. an ATS
// root that rejects is both a Leak and a Violation) and is intentionally not
// deduped.
func Score(rows []VerdictRow) Report {
	sc := GateScorecard{
		Leaks:         []string{},
		FalseCertains: []string{},
		Violations:    []Violation{},
	}
	for _, row := range rows {
		sc.Total++
		if row.Gate == GateUncertain {
			sc.LLMCalls++
		}
		if row.Label.Positive() && row.Gate == GateReject {
			sc.Leaks = append(sc.Leaks, row.URL)
		}
		if !row.Label.Positive() && row.Gate == GateCertainAccept {
			sc.FalseCertains = append(sc.FalseCertains, row.URL)
		}
		if want, ok := gateExpectation(row.Category); ok && row.Gate != want {
			sc.Violations = append(sc.Violations, Violation{
				URL:      row.URL,
				Category: row.Category,
				Want:     want,
				Got:      row.Gate,
			})
		}
	}
	sc.LLMCallRate = ratio(sc.LLMCalls, sc.Total)

	// LLM fold: score Confirm over ONLY the forwarded (GateUncertain) subset;
	// reject / certain-accept rows never reach the classifier.
	llm := [4]int{}
	for _, row := range rows {
		if row.Gate != GateUncertain {
			continue
		}
		confusion(&llm, row.LLMConfirm, row.Label.Positive())
	}

	// End-to-end fold: score the production decision over ALL rows, overall plus
	// one confusion matrix per category.
	overall := [4]int{}
	byCat := map[Category][4]int{}
	for _, row := range rows {
		predicted := endToEndPositive(row)
		gold := row.Label.Positive()
		confusion(&overall, predicted, gold)
		m := byCat[row.Category]
		confusion(&m, predicted, gold)
		byCat[row.Category] = m
	}
	byCategory := map[Category]ClassScore{}
	for cat, m := range byCat {
		byCategory[cat] = scoreClass(m[0], m[1], m[2], m[3])
	}

	return Report{
		Gate: sc,
		LLM:  LLMScorecard{ClassScore: scoreClass(llm[0], llm[1], llm[2], llm[3])},
		EndToEnd: EndToEndScorecard{
			Overall:    scoreClass(overall[0], overall[1], overall[2], overall[3]),
			ByCategory: byCategory,
		},
	}
}

// confusion increments the confusion-matrix quadrant for one (predicted, gold)
// pair. m indices are [tp, fp, fn, tn].
func confusion(m *[4]int, predicted, gold bool) {
	switch {
	case predicted && gold:
		m[0]++
	case predicted && !gold:
		m[1]++
	case !predicted && gold:
		m[2]++
	default:
		m[3]++
	}
}

// gateExpectation returns the structurally-fixed gate outcome a category must
// produce, or ok=false when the category has no hard rule. This is strictly
// stronger than the binary Leak/False-Certain checks: it catches an ATS root
// going uncertain, or an aggregator going uncertain-accept, which the binary
// rules miss.
func gateExpectation(c Category) (GateOutcome, bool) {
	switch c {
	case CategoryHubATSRoot:
		return GateCertainAccept, true
	case CategoryAggregator:
		return GateReject, true
	default:
		return 0, false
	}
}

// ratio is n/d rounded to four decimals, or 0 when d is 0. Mirrors the
// (unexported) ratio in internal/llmobs/stats.go so bench rates round identically.
func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return math.Round(float64(n)/float64(d)*10000) / 10000
}
