// Package inmem is the adapter for the in-memory url frontier.
// This is where the logic is implemented
package inmem

import (
	"context"
	"errors"
	"sync"
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

type FrontierOption func(*Frontier)

type Frontier struct {
	queues   map[string]*queue
	cooldown time.Duration
	mu       sync.Mutex
	signal   chan struct{}
}

func WithCooldown(c time.Duration) FrontierOption {
	return func(f *Frontier) {
		f.cooldown = c
	}
}

func NewFrontier(opts ...FrontierOption) *Frontier {
	f := &Frontier{
		queues:   map[string]*queue{},
		cooldown: time.Second,
		mu:       sync.Mutex{},
		signal:   make(chan struct{}, 1),
	}

	for _, fn := range opts {
		fn(f)
	}

	return f
}

func (f *Frontier) AddURL(ctx context.Context, url crawler.URL) error {
	f.mu.Lock()

	if _, ok := f.queues[url.Base]; !ok {
		f.queues[url.Base] = newQueue(f.cooldown)
	}

	f.queues[url.Base].push(url)

	f.mu.Unlock()

	select {
	case f.signal <- struct{}{}:
	default:
	}

	return nil
}

func (f *Frontier) Next(ctx context.Context) (crawler.URL, error) {
	for {
		f.mu.Lock()

		var nextDeadline *time.Time

		for _, q := range f.queues {
			if len(q.urls) == 0 {
				continue
			}

			if time.Until(q.deadline) <= 0 {
				url, _ := q.pop()
				q.deadline = time.Now().Add(f.cooldown)
				f.mu.Unlock()
				return url, nil
			}

			if nextDeadline == nil || q.deadline.Before(*nextDeadline) {
				nextDeadline = &q.deadline
			}
		}

		f.mu.Unlock()

		// there are still URLs in some of the queues,
		// wait until the next deadline is ready or a signal that a new url got added
		if nextDeadline != nil {
			select {
			case <-f.signal:
				continue
			case <-time.After(time.Until(*nextDeadline)):
				continue
			case <-ctx.Done():
				return crawler.URL{}, errors.New("still urls left, but context done")
			}
		}

		// no urls left in the queues, we are waiting for a signal that a new one got added
		select {
		case <-f.signal:
			continue
		case <-ctx.Done():
			return crawler.URL{}, errors.New("no urls found, and context done")
		}
	}
}
