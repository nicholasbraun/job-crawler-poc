package inmem

import (
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type queue struct {
	deadline time.Time
	urls     []crawler.URL
}

func newQueue() *queue {
	return &queue{
		deadline: time.Now(),
		urls:     []crawler.URL{},
	}
}

// push adds an element to the queue
func (q *queue) push(url crawler.URL) {
	q.urls = append(q.urls, url)
}

// pop returns the first element from the queue and removes it from the queue
func (q *queue) pop() (crawler.URL, bool) {
	if len(q.urls) == 0 {
		return crawler.URL{}, false
	}

	url := q.urls[0]
	q.urls[0] = crawler.URL{} // zero slot so GC can collect the strings
	q.urls = q.urls[1:]

	// compact: if len is less than half of cap, copy to a fresh slice
	if cap(q.urls) > 64 && len(q.urls) < cap(q.urls)/2 {
		shrunk := make([]crawler.URL, len(q.urls))
		copy(shrunk, q.urls)
		q.urls = shrunk
	}

	return url, true
}
