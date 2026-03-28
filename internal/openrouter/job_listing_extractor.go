// Package openrouter implements a JobListingExtractor that uses the
// OpenRouter chat completions API to extract structured job listing data
// from raw HTML content via an LLM.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

const (
	openrouterAPIURL = "https://openrouter.ai/api/v1/chat/completions"
	openrouterPrompt = `
	Parse the HTML below '____' and return only a valid json string with the following fields:
	- "title": title of the document. Usually inside the <h1></h1> tag (type: string)
	- "description": a short description of the job listing (type: string)
	- "company": the name of the company that this job listing is for (type: string)
	- "location": the location of the office were that job is available at (type: string)
	- "remote": if this job is available remotely (type: number: 1 for true, 0 for false)
	- "tech_stack": specific programming languages, frameworks, databases, 
cloud platforms, and tools mentioned (e.g. "Go", "PostgreSQL", "Kubernetes"). 
Do NOT include generic terms like "algorithms" or "data". (type: array of strings)
	____
	
	`
)

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// JobListingExtractor sends raw page content to the OpenRouter chat API
// and parses the LLM's JSON response into a JobListing.
type JobListingExtractor struct {
	apiKey string
}

func NewJobListingExtractor(apiKey string) *JobListingExtractor {
	return &JobListingExtractor{
		apiKey: apiKey,
	}
}

// Extract sends the main content of a raw job listing page to the OpenRouter
// chat completions API and unmarshals the LLM response into a JobListing.
// The returned JobListing.URL is set to the source page URL.
func (jle *JobListingExtractor) Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.JobListing, error) {
	reqBody := chatRequest{
		Model: "openai/gpt-5.4-nano",
		Messages: []message{
			{"user", strings.Join([]string{openrouterPrompt, raw.Content.MainContent}, "")},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return crawler.JobListing{}, fmt.Errorf("error marshaling request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openrouterAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return crawler.JobListing{}, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+jle.apiKey)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return crawler.JobListing{}, fmt.Errorf("error sending request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return crawler.JobListing{}, fmt.Errorf("openrouter: status %d: %s", res.StatusCode, body)
	}

	var chatRes chatResponse
	if err := json.NewDecoder(res.Body).Decode(&chatRes); err != nil {
		return crawler.JobListing{}, fmt.Errorf("error decoding openrouter response: %w", err)
	}

	if len(chatRes.Choices) == 0 {
		return crawler.JobListing{}, fmt.Errorf("openrouter: empty response")
	}

	content := chatRes.Choices[0].Message.Content
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var jobListing crawler.JobListing
	if err := json.Unmarshal([]byte(content), &jobListing); err != nil {
		return crawler.JobListing{}, fmt.Errorf("error parsing job listing JSON: %w", err)
	}

	jobListing.URL = raw.URL.RawURL

	return jobListing, nil
}
