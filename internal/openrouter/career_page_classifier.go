package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

const careerPagePrompt = `
You are given the URL, title, and main text of a web page. Decide whether this
page is a company Career Page: a page whose purpose is to list a single company's
open job positions (a careers or jobs hub / openings list), linking out to the
individual postings. It may list one or many roles and may be paginated — a page
that lists a single opening is still a Career Page.

A single job-posting or job-description page (the full details of ONE role) is
NOT a career page — it is one listing under a career page. A generic homepage, a
blog post, a news article, or a third-party job-board aggregator is also NOT a
career page.

Return ONLY a valid JSON object with a single boolean field, no prose:
{"is_career_page": true} or {"is_career_page": false}
`

// careerPageConfirmation is the LLM's structured verdict.
type careerPageConfirmation struct {
	IsCareerPage bool `json:"is_career_page"`
}

// CareerPageClassifier asks the OpenRouter chat API to confirm whether a
// gate-passing candidate is really a company Career Page. It is consulted only
// for candidates that lack a JobPosting JSON-LD, bounding LLM cost at perpetual
// discovery scale.
type CareerPageClassifier struct {
	apiKey     string
	httpClient *http.Client
}

func NewCareerPageClassifier(apiKey string) *CareerPageClassifier {
	return &CareerPageClassifier{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Confirm sends the candidate page's URL, title, and main content to the LLM
// and returns its yes/no career-page verdict.
func (c *CareerPageClassifier) Confirm(ctx context.Context, url string, content *crawler.Content) (bool, error) {
	userContent := fmt.Sprintf("URL: %s\nTitle: %s\n\n%s", url, content.Title, content.MainContent)
	reqBody := chatRequest{
		Model: "openai/gpt-5.4-nano",
		Messages: []message{
			{"system", careerPagePrompt},
			{"user", userContent},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return false, fmt.Errorf("error marshaling request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openrouterAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return false, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("error sending request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return false, fmt.Errorf("openrouter: status %d: %s", res.StatusCode, body)
	}

	const maxResponseBytes = 1 << 20 // 1 MB
	var chatRes chatResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(&chatRes); err != nil {
		return false, fmt.Errorf("error decoding openrouter response: %w", err)
	}

	if len(chatRes.Choices) == 0 {
		return false, fmt.Errorf("openrouter: empty response")
	}

	content0 := chatRes.Choices[0].Message.Content
	content0 = strings.TrimSpace(content0)
	content0 = strings.TrimPrefix(content0, "```json")
	content0 = strings.TrimPrefix(content0, "```")
	content0 = strings.TrimSuffix(content0, "```")
	content0 = strings.TrimSpace(content0)

	var confirmation careerPageConfirmation
	if err := json.Unmarshal([]byte(content0), &confirmation); err != nil {
		return false, fmt.Errorf("error parsing career page confirmation JSON: %w", err)
	}

	return confirmation.IsCareerPage, nil
}
