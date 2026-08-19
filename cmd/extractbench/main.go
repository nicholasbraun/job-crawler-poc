// Package main is extractbench: it runs the REAL openrouter.JobListingExtractor
// over the Extract Gold Set and scores its is_job_posting verdict against the
// human labels, plus its field output against expected.tsv. It exists because no
// llmbench verb exercises the extraction system prompt -- bench drives the
// career-page classifier, extract/score-capture are gate-only, and score-free
// stubs the model -- so a prompt regression reaches production unmeasured.
//
// It reads rows from the gold set (-in, narrowed by -urls or -per-class) or parses
// an llmbench HTML fixture set (-fixtures) with the real parser, and drives the
// model through openrouter.ConfigFromEnv so a run cannot disagree with the crawl
// on a default. -base-url, -model and -max-chars override single knobs for a local
// server; the header prints what was actually used.
//
//	go run ./cmd/extractbench -per-class 25            # 25 rows per label class
//	go run ./cmd/extractbench -urls rows.txt -out r.jsonl
//
// THIS IS A MEASURING INSTRUMENT, NOT A GATE. Unlike every llmbench verb it always
// exits 0, so a false-drop is reported and not enforced; it hand-rolls its
// scorecard instead of bench.ScoreExtract, so the numbers are not comparable with
// the extract scorecards nor diffable by llmbench diff; it emits no -json; and it
// reads the gold set itself rather than through readGoldSet, so a substrate format
// change breaks it silently. Folding it into llmbench as a proper verb is tracked
// in #276 -- until then a regression is caught only when a human runs this and
// reads the output.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/env"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

type goldRow struct {
	URL     string          `json:"url"`
	Label   string          `json:"label"`
	Stratum string          `json:"stratum"`
	Weight  float64         `json:"weight"`
	Content crawler.Content `json:"content"`
}

type expected struct {
	Title           string
	Location        string
	WorkArrangement string
}

type result struct {
	URL             string  `json:"url"`
	Label           string  `json:"label"`
	Stratum         string  `json:"stratum"`
	IsJobPosting    bool    `json:"is_job_posting"`
	Title           string  `json:"title"`
	Company         string  `json:"company"`
	Location        string  `json:"location"`
	WorkArrangement string  `json:"work_arrangement"`
	Err             string  `json:"err,omitempty"`
	Seconds         float64 `json:"seconds"`
}

func main() {
	in := flag.String("in", "cmd/llmbench/extract-goldset/goldset.jsonl", "gold set jsonl")
	exp := flag.String("expected", "cmd/llmbench/extract-goldset/expected.tsv", "expected field values tsv")
	out := flag.String("out", "", "write per-row results as JSONL here")
	workers := flag.Int("workers", 1, "concurrent extract calls")
	perClass := flag.Int("per-class", 0, "deterministic subsample: at most N rows per label class (0 = all)")
	tag := flag.String("tag", "", "label for the run, printed in the header")
	fixtures := flag.String("fixtures", "", "run the HTML fixtures in this dir (manifest.json + pages/) through the real parser instead of -in")
	urlList := flag.String("urls", "", "restrict the run to the URLs in this file (one per line); overrides -per-class")
	baseURL := flag.String("base-url", "", "override LLM_BASE_URL (e.g. swap host.docker.internal for localhost when running outside Docker)")
	model := flag.String("model", "", "override LLM_MODEL (the local server may have a different one loaded)")
	maxChars := flag.Int("max-chars", 0, "override LLM_EXTRACT_MAX_CHARS")
	flag.Parse()

	var rows []goldRow
	var err error
	if *fixtures != "" {
		rows, err = loadFixtures(*fixtures)
	} else {
		rows, err = loadGold(*in)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "load rows:", err)
		os.Exit(2)
	}
	expByURL, err := loadExpected(*exp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load expected:", err)
		os.Exit(2)
	}
	if *urlList != "" {
		b, err := os.ReadFile(*urlList)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read url list:", err)
			os.Exit(2)
		}
		want := map[string]bool{}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				want[l] = true
			}
		}
		var keep []goldRow
		for _, r := range rows {
			if want[r.URL] {
				keep = append(keep, r)
			}
		}
		rows = keep
	} else {
		rows = subsample(rows, *perClass)
	}

	// Best-effort .env from the working directory, exactly as llmbench's loadLLMConfig
	// does it -- so a bench run and a crawl cannot disagree on a default.
	_ = godotenv.Load()
	var ld env.Loader
	cfg := openrouter.ConfigFromEnv(&ld)
	if err := ld.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "llm config:", err)
		os.Exit(2)
	}
	// LLM_BASE_URL commonly carries the crawler-in-docker spelling
	// (host.docker.internal); -base-url points the same run at the host's own view of
	// that server. It is an explicit flag rather than a silent rewrite so the header
	// always prints the endpoint actually used.
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if *model != "" {
		cfg.Model = *model
	}
	if *maxChars > 0 {
		cfg.ExtractMaxChars = *maxChars
	}

	fmt.Printf("== extractbench %s ==\nmodel=%s base=%s rows=%d workers=%d extract_max_chars=%d\n\n",
		*tag, cfg.Model, cfg.BaseURL, len(rows), *workers, cfg.ExtractMaxChars)

	ex := openrouter.NewJobListingExtractor(cfg)
	results := make([]result, len(rows))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *workers)
	var done int
	var mu sync.Mutex
	start := time.Now()
	for i, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, r goldRow) {
			defer wg.Done()
			defer func() { <-sem }()
			t0 := time.Now()
			e, err := ex.Extract(context.Background(), crawler.RawJobListing{
				URL:     crawler.URL{RawURL: r.URL},
				Content: r.Content,
			})
			res := result{URL: r.URL, Label: r.Label, Stratum: r.Stratum, Seconds: time.Since(t0).Seconds()}
			if err != nil {
				res.Err = err.Error()
			} else {
				res.IsJobPosting = e.IsJobPosting
				res.Title = e.Listing.Title
				res.Company = e.Listing.Company
				res.Location = e.Listing.Location
				res.WorkArrangement = string(e.Listing.WorkArrangement)
			}
			results[i] = res
			mu.Lock()
			done++
			if done%10 == 0 || done == len(rows) {
				fmt.Fprintf(os.Stderr, "\r%d/%d (%.0fs elapsed)", done, len(rows), time.Since(start).Seconds())
			}
			mu.Unlock()
		}(i, r)
	}
	wg.Wait()
	fmt.Fprintln(os.Stderr)

	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "out:", err)
			os.Exit(2)
		}
		enc := json.NewEncoder(f)
		for _, r := range results {
			_ = enc.Encode(r)
		}
		_ = f.Close()
	}
	report(results, expByURL, time.Since(start))
}

func loadGold(path string) ([]goldRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	var rows []goldRow
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r goldRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		switch r.Label {
		case "detail", "hub-index", "residue":
			rows = append(rows, r)
		}
	}
	return rows, sc.Err()
}

func loadExpected(path string) (map[string]expected, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]expected{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c := strings.Split(line, "\t")
		if len(c) < 6 {
			continue
		}
		m[c[1]] = expected{Title: c[3], Location: c[4], WorkArrangement: c[5]}
	}
	return m, nil
}

// subsample keeps at most n rows per label class, spread evenly across the file
// so the pick is deterministic and not front-loaded on one stratum.
func subsample(rows []goldRow, n int) []goldRow {
	if n <= 0 {
		return rows
	}
	byClass := map[string][]goldRow{}
	for _, r := range rows {
		byClass[r.Label] = append(byClass[r.Label], r)
	}
	var keep []goldRow
	classes := make([]string, 0, len(byClass))
	for k := range byClass {
		classes = append(classes, k)
	}
	sort.Strings(classes)
	for _, c := range classes {
		rs := byClass[c]
		if len(rs) <= n {
			keep = append(keep, rs...)
			continue
		}
		step := float64(len(rs)) / float64(n)
		for i := 0; i < n; i++ {
			keep = append(keep, rs[int(float64(i)*step)])
		}
	}
	return keep
}

func report(rs []result, expByURL map[string]expected, elapsed time.Duration) {
	var detail, detailYes, neg, negNo, errs int
	var hub, hubNo, res, resNo int
	var falseDrops, leaks []string
	for _, r := range rs {
		if r.Err != "" {
			errs++
			continue
		}
		switch r.Label {
		case "detail":
			detail++
			if r.IsJobPosting {
				detailYes++
			} else {
				falseDrops = append(falseDrops, r.URL)
			}
		default:
			neg++
			if !r.IsJobPosting {
				negNo++
			} else {
				leaks = append(leaks, r.URL+"  ["+r.Label+"]")
			}
			if r.Label == "hub-index" {
				hub++
				if !r.IsJobPosting {
					hubNo++
				}
			} else {
				res++
				if !r.IsJobPosting {
					resNo++
				}
			}
		}
	}
	tp, fn := detailYes, detail-detailYes
	fp := neg - negNo
	prec, rec := pct(tp, tp+fp), pct(tp, tp+fn)
	fmt.Printf("VERDICT (is_job_posting) — %d scored, %d errors, %s wall\n", len(rs)-errs, errs, elapsed.Round(time.Second))
	fmt.Printf("  detail   recall   %6.1f%%  (%d/%d kept)      <- false-drops: %d\n", rec, detailYes, detail, fn)
	fmt.Printf("  hub-index shed    %6.1f%%  (%d/%d)\n", pct(hubNo, hub), hubNo, hub)
	fmt.Printf("  residue   shed    %6.1f%%  (%d/%d)\n", pct(resNo, res), resNo, res)
	fmt.Printf("  precision         %6.1f%%  (%d kept were detail of %d kept)\n", prec, tp, tp+fp)
	if prec+rec > 0 {
		fmt.Printf("  F1                %6.1f%%\n", 2*prec*rec/(prec+rec))
	}
	fmt.Printf("  save rate         %6.1f%%  (%d/%d pages the extractor would save)\n\n", pct(tp+fp, len(rs)-errs), tp+fp, len(rs)-errs)

	// Field accuracy over the confirmed expected values (detail rows only).
	var fieldsN, titleOK, locOK, waOK, countryNamed int
	for _, r := range rs {
		e, ok := expByURL[r.URL]
		if !ok || r.Err != "" || !r.IsJobPosting {
			continue
		}
		fieldsN++
		if norm(r.Title) == norm(e.Title) {
			titleOK++
		}
		if norm(r.Location) == norm(e.Location) {
			locOK++
		}
		if strings.EqualFold(r.WorkArrangement, e.WorkArrangement) {
			waOK++
		}
		if r.Location != "" && !hasCountryCodeTail(r.Location) {
			countryNamed++
		}
	}
	if fieldsN > 0 {
		fmt.Printf("FIELDS (vs expected.tsv, %d rows extracted+confirmed)\n", fieldsN)
		fmt.Printf("  title exact       %6.1f%%  (%d/%d)\n", pct(titleOK, fieldsN), titleOK, fieldsN)
		fmt.Printf("  location exact    %6.1f%%  (%d/%d)\n", pct(locOK, fieldsN), locOK, fieldsN)
		fmt.Printf("  work_arrangement  %6.1f%%  (%d/%d)\n", pct(waOK, fieldsN), waOK, fieldsN)
		fmt.Printf("  country in words  %6.1f%%  (%d/%d)\n\n", pct(countryNamed, fieldsN), countryNamed, fieldsN)
	}
	if len(falseDrops) > 0 {
		fmt.Printf("FALSE DROPS (%d) — real postings the extractor abstained on:\n", len(falseDrops))
		for _, u := range falseDrops {
			fmt.Println("  " + u)
		}
		fmt.Println()
	}
	if len(leaks) > 0 {
		fmt.Printf("LEAKS (%d) — non-postings the extractor would save:\n", len(leaks))
		for _, u := range leaks {
			fmt.Println("  " + u)
		}
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// hasCountryCodeTail flags a location whose last comma-separated part looks like
// an ISO country code rather than a country name -- the prompt forbids it.
func hasCountryCodeTail(loc string) bool {
	parts := strings.Split(loc, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	return len(last) == 2 && last == strings.ToUpper(last)
}

// loadFixtures reads an llmbench HTML fixture set (manifest.json + pages/) and
// parses each page with the real parser, so a fixture reaches the extractor as the
// same *Content the crawl would have produced.
func loadFixtures(dir string) ([]goldRow, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest []struct {
		File  string `json:"file"`
		URL   string `json:"url"`
		Label string `json:"label"`
		Note  string `json:"note"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, err
	}
	p := parser.NewHTMLParser()
	var rows []goldRow
	for _, m := range manifest {
		switch m.Label {
		case "detail", "hub-index", "residue":
		default:
			continue
		}
		html, err := os.ReadFile(filepath.Join(dir, "pages", m.File))
		if err != nil {
			return nil, err
		}
		c, err := p.Parse(html)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", m.File, err)
		}
		rows = append(rows, goldRow{URL: m.URL, Label: m.Label, Stratum: "fixture", Content: *c})
	}
	return rows, nil
}
