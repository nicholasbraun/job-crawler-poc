package inmem

import (
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type queue struct {
	deadline time.Time
	cooldown time.Duration
	urls     []crawler.URL
}

func newQueue(cooldown time.Duration) *queue {
	return &queue{
		deadline: time.Now(),
		cooldown: cooldown,
		urls:     []crawler.URL{},
	}
}

// push adds an element to the queue
func (q *queue) push(url crawler.URL) {
	q.urls = append(q.urls, url)
}

// pop returns the first element from the queue and removes it from the queue
func (q *queue) pop() (crawler.URL, bool) {
	if len(q.urls) > 0 {
		url := q.urls[0]
		q.urls = q.urls[1:]

		return url, true
	}

	return crawler.URL{}, false
}
