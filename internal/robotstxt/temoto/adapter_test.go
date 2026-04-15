package temoto_test

import (
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt/temoto"
)

const fixture = `User-agent: TestBot
Disallow: /private/
Allow: /private/public/
Crawl-delay: 5

User-agent: *
Disallow: /
`

func TestParseAppliesMatchingGroup(t *testing.T) {
	parser := temoto.NewRobotsTxtParser("TestBot")

	rules, err := parser.Parse([]byte(fixture))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	tests := []struct {
		path string
		want bool
	}{
		{"/public", true},
		{"/private/secret", false},
		{"/private/public/thing", true},
		{"/", true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := rules.IsAllowed(tt.path); got != tt.want {
				t.Fatalf("IsAllowed(%q): got %v want %v", tt.path, got, tt.want)
			}
		})
	}

	if got := rules.CrawlDelay(); got != 5*time.Second {
		t.Fatalf("CrawlDelay: got %v want %v", got, 5*time.Second)
	}
}

func TestParseFallsBackToWildcardGroup(t *testing.T) {
	parser := temoto.NewRobotsTxtParser("OtherBot")

	rules, err := parser.Parse([]byte(fixture))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if rules.IsAllowed("/anything") {
		t.Fatalf("wildcard group disallows /, but /anything was allowed")
	}
}

func TestParseEmptyInputAllowsEverything(t *testing.T) {
	parser := temoto.NewRobotsTxtParser("TestBot")

	rules, err := parser.Parse(nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !rules.IsAllowed("/anything") {
		t.Fatalf("empty robots.txt should allow everything")
	}
}
