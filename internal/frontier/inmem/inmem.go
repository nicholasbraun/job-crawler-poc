// Package inmem is the adapter for the in-memory url frontier.
// This is where the logic is implemented
package inmem

import (
	"context"
	"log/slog"
	"sync"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
)

type FrontierOption func(*Frontier)

type Frontier struct {
	queues     map[string]*queue
	cooldown   time.Duration
	mu         sync.Mutex
	signal     chan struct{}
	maxDomains int
}

var _ frontier.Frontier = &Frontier{}

func WithCooldown(c time.Duration) FrontierOption {
	return func(f *Frontier) {
		f.cooldown = c
	}
}

func WithMaxDomains(md int) FrontierOption {
	return func(f *Frontier) {
		f.maxDomains = md
	}
}

func NewFrontier(opts ...FrontierOption) *Frontier {
	f := &Frontier{
		queues:     map[string]*queue{},
		cooldown:   time.Second,
		maxDomains: 50,
		mu:         sync.Mutex{},
		signal:     make(chan struct{}, 1),
	}

	for _, fn := range opts {
		fn(f)
	}

	slog.Info("created new Frontier", "frontier", f)

	return f
}

// AddURL adds an URL to the frontier
func (f *Frontier) AddURL(ctx context.Context, url crawler.URL) error {
	f.mu.Lock()

	if _, ok := f.queues[url.Hostname]; !ok {
		if len(f.queues) >= f.maxDomains {
			f.mu.Unlock()
			return frontier.ErrMaxDomainLimit
		}
		f.queues[url.Hostname] = newQueue()
	}

	f.queues[url.Hostname].push(url)

	f.mu.Unlock()

	select {
	case f.signal <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	slog.Info("added URL to the frontier", "url", url.RawURL)
	return nil
}

// Next blocks until the next URL is available and returns it from the frontier
func (f *Frontier) Next(ctx context.Context) (crawler.URL, error) {
	for {
		f.mu.Lock()

		var nextDeadline *time.Time

		for _, q := range f.queues {
			if len(q.urls) == 0 {
				continue
			}

			// this queue's deadline has elapsed and we can return an URL from it
			if time.Until(q.deadline) <= 0 {
				url, _ := q.pop()
				q.deadline = time.Now().Add(f.cooldown)
				f.mu.Unlock()
				return url, nil
			}

			// find the next deadline of all queues with URLs
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
				return crawler.URL{}, ctx.Err()
			}
		}

		// no urls left in the queues, signal work done
		return crawler.URL{}, frontier.ErrDone
	}
}
