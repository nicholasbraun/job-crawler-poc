// Package temoto implements robotstxt/Parser
package temoto

import (
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
	temotorobotstxt "github.com/temoto/robotstxt"
)

type RobotsTxtParser struct {
	userAgent string
}

type Rules struct {
	group *temotorobotstxt.Group
}

func NewRobotsTxtParser(userAgent string) *RobotsTxtParser {
	return &RobotsTxtParser{
		userAgent: userAgent,
	}
}

var (
	_ robotstxt.Parser = &RobotsTxtParser{}
	_ robotstxt.Rules  = &Rules{}
)

func (p *RobotsTxtParser) Parse(b []byte) (robotstxt.Rules, error) {
	robots, err := temotorobotstxt.FromBytes(b)
	if err != nil {
		return nil, err
	}

	group := robots.FindGroup(p.userAgent)

	rules := &Rules{
		group: group,
	}

	return rules, nil
}

func (r *Rules) IsAllowed(path string) bool {
	return r.group.Test(path)
}

func (r *Rules) CrawlDelay() time.Duration {
	return r.group.CrawlDelay
}
