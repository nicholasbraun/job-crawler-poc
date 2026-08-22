package llmobs_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installLLMReader points the process-global meter provider at a fresh ManualReader
// for the duration of the test, restoring the previous provider on cleanup. LLM
// instruments bind at construction, so NewMetrics must be called AFTER this and the
// test must be non-parallel — the same manual-reader discipline the collection and
// frontier metrics tests use.
func installLLMReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
}

// llmMetric returns the named instrument on the "llm" scope, or nil when the
// instrument recorded nothing (an untouched counter is never exported). The manual
// reader reports the raw dotted name; Prometheus exports crawler.llm.shadow as
// crawler_llm_shadow_total.
func llmMetric(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "llm" {
			continue
		}
		for i, m := range sm.Metrics {
			if m.Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

// TestMetricsShadowIsCountedSeparatelyFromCalls closes the loop from Recorder.Shadow
// to the exported instrument, and pins the separation ADR-0044 rests on: measurement
// spend lands on crawler.llm.shadow, labelled by verdict AND by the gate rung that
// rejected the page, and never on crawler.llm.calls — folding it in would corrupt the
// extract call rate the Extract Gate is judged by. The rung split is what lets the
// post-merge reading tell a drop the Positive Evidence kill switch would undo from
// one it would not. Non-parallel: the manual reader is the process-global meter
// provider and instruments bind at NewMetrics.
func TestMetricsShadowIsCountedSeparatelyFromCalls(t *testing.T) {
	reader := installLLMReader(t)
	metrics := llmobs.NewMetrics()
	rec := llmobs.NewRecorder(metrics, nil, nil, "")

	ctx := t.Context()
	rec.Shadow(ctx, llmobs.ShadowAccept, "positive_evidence")
	rec.Shadow(ctx, llmobs.ShadowAccept, "job_link_saturation")
	rec.Shadow(ctx, llmobs.ShadowAbstain, "positive_evidence")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	shadow := llmMetric(&rm, "crawler.llm.shadow")
	if shadow == nil {
		t.Fatal("crawler.llm.shadow instrument not found on the llm scope")
	}
	sum, ok := shadow.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("crawler.llm.shadow: unexpected data type %T", shadow.Data)
	}

	byVerdict := map[string]int64{}
	byVerdictRung := map[string]int64{}
	for _, dp := range sum.DataPoints {
		v, present := dp.Attributes.Value("verdict")
		if !present {
			t.Errorf("a shadow data point carries no verdict attribute: %v", dp.Attributes)
			continue
		}
		r, present := dp.Attributes.Value("rung")
		if !present {
			t.Errorf("a shadow data point carries no rung attribute: %v", dp.Attributes)
			continue
		}
		byVerdict[v.Emit()] += dp.Value
		byVerdictRung[v.Emit()+"/"+r.Emit()] += dp.Value
	}
	want := map[string]int64{"accept": 2, "abstain": 1}
	for verdict, n := range want {
		if byVerdict[verdict] != n {
			t.Errorf("shadow[verdict=%s] = %d, want %d (all: %v)", verdict, byVerdict[verdict], n, byVerdict)
		}
	}
	// The split the post-merge reading turns on: an accept under the Positive Evidence
	// rung is a drop the kill switch would recover, one under a reject rung is not.
	wantSplit := map[string]int64{
		"accept/positive_evidence":   1,
		"accept/job_link_saturation": 1,
		"abstain/positive_evidence":  1,
	}
	for key, n := range wantSplit {
		if byVerdictRung[key] != n {
			t.Errorf("shadow[%s] = %d, want %d (all: %v)", key, byVerdictRung[key], n, byVerdictRung)
		}
	}

	// The load-bearing assertion: a Shadow Extraction is real spend, but the call
	// counter must keep meaning "calls the Extract Gate admitted".
	if calls := llmMetric(&rm, "crawler.llm.calls"); calls != nil {
		if callSum, ok := calls.Data.(metricdata.Sum[int64]); ok && len(callSum.DataPoints) > 0 {
			t.Errorf("crawler.llm.calls moved on a Shadow Extraction: %v", callSum.DataPoints)
		}
	}
}

// TestMetricsShadowDroppedIsItsOwnCounter pins what makes the live false-drop rate
// readable: a sample the bounded shadow stream sheds is counted, keyed by the same
// rung, and never lands on crawler.llm.shadow — a sample with no verdict must not
// enter that counter's denominator. Non-parallel for the same reason as the test
// above.
func TestMetricsShadowDroppedIsItsOwnCounter(t *testing.T) {
	reader := installLLMReader(t)
	metrics := llmobs.NewMetrics()
	rec := llmobs.NewRecorder(metrics, nil, nil, "")

	ctx := t.Context()
	rec.ShadowDropped(ctx, "positive_evidence")
	rec.ShadowDropped(ctx, "positive_evidence")
	rec.ShadowDropped(ctx, "reject_path")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	dropped := llmMetric(&rm, "crawler.llm.shadow.dropped")
	if dropped == nil {
		t.Fatal("crawler.llm.shadow.dropped instrument not found on the llm scope")
	}
	sum, ok := dropped.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("crawler.llm.shadow.dropped: unexpected data type %T", dropped.Data)
	}
	byRung := map[string]int64{}
	for _, dp := range sum.DataPoints {
		r, present := dp.Attributes.Value("rung")
		if !present {
			t.Errorf("a dropped data point carries no rung attribute: %v", dp.Attributes)
			continue
		}
		byRung[r.Emit()] += dp.Value
	}
	for rung, n := range map[string]int64{"positive_evidence": 2, "reject_path": 1} {
		if byRung[rung] != n {
			t.Errorf("shadow.dropped[rung=%s] = %d, want %d (all: %v)", rung, byRung[rung], n, byRung)
		}
	}

	if shadow := llmMetric(&rm, "crawler.llm.shadow"); shadow != nil {
		if shadowSum, ok := shadow.Data.(metricdata.Sum[int64]); ok && len(shadowSum.DataPoints) > 0 {
			t.Errorf("crawler.llm.shadow moved on a shed sample: %v", shadowSum.DataPoints)
		}
	}
}

// TestPrimeShadowMakesTheFirstSampleVisible pins what priming is FOR, which is not
// "the series exist" but "the first increment is observable". A counter that first
// appears already holding 1 shows an increase() of 0 over every window, because
// increase() measures growth from the first scraped sample -- so without this the
// first false-drop on a rung, the event the whole lane exists to catch, is silently
// absorbed as the series' baseline. Live verification of PR #272 hit exactly that:
// the raw counter read 1 accept while every range-scoped panel read 0.
//
// The test states it as a before/after observation, the way a scrape would see it:
// a primed series is present at 0 first, and reads 1 after one sample.
func TestPrimeShadowMakesTheFirstSampleVisible(t *testing.T) {
	reader := installLLMReader(t)
	metrics := llmobs.NewMetrics()
	rec := llmobs.NewRecorder(metrics, nil, nil, "")
	ctx := t.Context()

	metrics.PrimeShadow(ctx, []string{"positive_evidence", "index_terminal"})

	value := func(t *testing.T, name, rung, verdict string) (int64, bool) {
		t.Helper()
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &rm); err != nil {
			t.Fatalf("collecting metrics: %v", err)
		}
		m := llmMetric(&rm, name)
		if m == nil {
			return 0, false
		}
		sum, ok := m.Data.(metricdata.Sum[int64])
		if !ok {
			t.Fatalf("%s: unexpected data type %T", name, m.Data)
		}
		for _, dp := range sum.DataPoints {
			r, hasRung := dp.Attributes.Value("rung")
			if !hasRung || r.Emit() != rung {
				continue
			}
			if verdict == "" {
				return dp.Value, true
			}
			if v, has := dp.Attributes.Value("verdict"); has && v.Emit() == verdict {
				return dp.Value, true
			}
		}
		return 0, false
	}

	// The scrape a fresh deploy takes BEFORE anything happens: the series must exist,
	// holding zero. Without priming there is nothing here at all, and that absence is
	// what swallows the first sample.
	for _, c := range []struct{ name, rung, verdict string }{
		{"crawler.llm.shadow", "positive_evidence", "accept"},
		{"crawler.llm.shadow", "index_terminal", "accept"},
		{"crawler.llm.shadow", "positive_evidence", "abstain"},
		{"crawler.llm.shadow.dropped", "positive_evidence", ""},
	} {
		got, present := value(t, c.name, c.rung, c.verdict)
		if !present {
			t.Errorf("%s{rung=%s,verdict=%s} is absent before any sample; priming did not create it", c.name, c.rung, c.verdict)
			continue
		}
		if got != 0 {
			t.Errorf("%s{rung=%s,verdict=%s} = %d before any sample, want 0", c.name, c.rung, c.verdict, got)
		}
	}

	// One sample. The series must now read 1, i.e. the growth from the primed zero is
	// observable rather than being the series' first-ever value.
	rec.Shadow(ctx, llmobs.ShadowAccept, "positive_evidence")
	rec.ShadowDropped(ctx, "positive_evidence")

	if got, present := value(t, "crawler.llm.shadow", "positive_evidence", "accept"); !present || got != 1 {
		t.Errorf("shadow{rung=positive_evidence,verdict=accept} = %d (present=%v) after one sample, want 1", got, present)
	}
	if got, present := value(t, "crawler.llm.shadow.dropped", "positive_evidence", ""); !present || got != 1 {
		t.Errorf("shadow.dropped{rung=positive_evidence} = %d (present=%v) after one shed sample, want 1", got, present)
	}
	// A rung that never fires stays at zero rather than vanishing -- that is the
	// reading "this rung has dropped nothing", which an absent series cannot make.
	if got, present := value(t, "crawler.llm.shadow", "index_terminal", "accept"); !present || got != 0 {
		t.Errorf("shadow{rung=index_terminal,verdict=accept} = %d (present=%v), want a present zero", got, present)
	}
}

// TestMetricsPostingScoreIsAnUnlabelledHistogram pins the instrument ADR-0049 asks for
// and the constraint it puts on it: the Posting Score is recorded as a distribution
// and appears in NO metric label value anywhere.
//
// The label ban is not fastidiousness. Shadow Extraction samples ~1% of gate rejects
// filed by rung, so bucketing the score into low/mid/high series would divide those
// already-sparse samples several ways and multiply the time to observe the veto's
// first false-drop — the exact failure PrimeShadow exists to prevent. A histogram's own
// `le` boundaries are not that split: they are the instrument, and they cost no series
// on any counter.
//
// The explicit bucket ladder is asserted too, because OTel's defaults run to 10,000 and
// would file every score in [0,1] into the first bucket, leaving the distribution
// unreadable. Non-parallel: the manual reader is the process-global meter provider and
// instruments bind at NewMetrics.
func TestMetricsPostingScoreIsAnUnlabelledHistogram(t *testing.T) {
	reader := installLLMReader(t)
	metrics := llmobs.NewMetrics()
	rec := llmobs.NewRecorder(metrics, nil, nil, "")

	ctx := t.Context()
	scores := []float64{0.02, 0.31, 0.58, 0.61, 0.87}
	for _, score := range scores {
		rec.PostingScore(ctx, score)
	}
	// The veto's companion readings, so the attribute-key assertions below cover the
	// counters an implementation might be tempted to hang a score band on.
	rec.Gated(ctx, llmobs.KindExtract, llmobs.ReasonLearnedVeto)
	rec.Shadow(ctx, llmobs.ShadowAccept, "learned_veto")
	rec.ShadowDropped(ctx, "learned_veto")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	score := llmMetric(&rm, "crawler.llm.posting.score")
	if score == nil {
		t.Fatal("crawler.llm.posting.score instrument not found on the llm scope")
	}
	hist, ok := score.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("crawler.llm.posting.score: unexpected data type %T, want a float64 histogram", score.Data)
	}
	if len(hist.DataPoints) != 1 {
		t.Fatalf("crawler.llm.posting.score has %d data points, want exactly 1: the score is never split into series", len(hist.DataPoints))
	}
	dp := hist.DataPoints[0]
	if dp.Attributes.Len() != 0 {
		t.Errorf("crawler.llm.posting.score carries attributes %v, want none: no Posting Score value may appear in a label", dp.Attributes)
	}
	if dp.Count != uint64(len(scores)) {
		t.Errorf("crawler.llm.posting.score count = %d, want %d", dp.Count, len(scores))
	}
	wantBounds := []float64{
		0.05, 0.10, 0.15, 0.20, 0.25, 0.30, 0.35, 0.40, 0.45, 0.50,
		0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85, 0.90, 0.95,
	}
	if !reflect.DeepEqual(dp.Bounds, wantBounds) {
		t.Errorf("crawler.llm.posting.score bounds = %v, want %v: OTel's defaults would file every score into the first bucket",
			dp.Bounds, wantBounds)
	}

	// No counter grew a score-derived label either.
	wantKeys := map[string][]string{
		"crawler.llm.shadow":         {"rung", "verdict"},
		"crawler.llm.gated":          {"kind", "reason"},
		"crawler.llm.shadow.dropped": {"rung"},
	}
	for name, want := range wantKeys {
		got := attributeKeys(t, &rm, name)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s attribute keys = %v, want exactly %v: the Posting Score must not become a label on any counter", name, got, want)
		}
	}
}

// attributeKeys returns the sorted, de-duplicated attribute keys across every data
// point of the named int64-sum instrument.
func attributeKeys(t *testing.T, rm *metricdata.ResourceMetrics, name string) []string {
	t.Helper()
	m := llmMetric(rm, name)
	if m == nil {
		t.Fatalf("%s instrument not found on the llm scope", name)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: unexpected data type %T", name, m.Data)
	}
	seen := map[string]bool{}
	for _, dp := range sum.DataPoints {
		for _, kv := range dp.Attributes.ToSlice() {
			seen[string(kv.Key)] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestMetricsGatedSeparatesTheLearnedVetoFromTheStructuralReason pins the split
// ADR-0049 requires on the gated counter: the Learned Veto's saving is its own series,
// not pooled into url_structure. It is the one rung a single environment variable
// restores, so what pulling that switch would cost has to be readable on its own —
// pooled with every structural reject it is not readable at all.
func TestMetricsGatedSeparatesTheLearnedVetoFromTheStructuralReason(t *testing.T) {
	reader := installLLMReader(t)
	metrics := llmobs.NewMetrics()
	rec := llmobs.NewRecorder(metrics, nil, nil, "")

	ctx := t.Context()
	rec.Gated(ctx, llmobs.KindExtract, llmobs.ReasonLearnedVeto)
	rec.Gated(ctx, llmobs.KindExtract, llmobs.ReasonLearnedVeto)
	rec.Gated(ctx, llmobs.KindExtract, llmobs.ReasonURLStructure)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	gated := llmMetric(&rm, "crawler.llm.gated")
	if gated == nil {
		t.Fatal("crawler.llm.gated instrument not found on the llm scope")
	}
	sum, ok := gated.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("crawler.llm.gated: unexpected data type %T", gated.Data)
	}
	byReason := map[string]int64{}
	for _, dp := range sum.DataPoints {
		r, present := dp.Attributes.Value("reason")
		if !present {
			t.Errorf("a gated data point carries no reason attribute: %v", dp.Attributes)
			continue
		}
		byReason[r.Emit()] += dp.Value
	}
	want := map[string]int64{"learned_veto": 2, "url_structure": 1}
	if !reflect.DeepEqual(byReason, want) {
		t.Errorf("gated by reason = %v, want %v", byReason, want)
	}
}
