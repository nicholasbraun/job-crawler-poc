// Package inmem implements an in-memory URL frontier with per-domain queues,
// configurable cooldowns, and in-flight tracking. Next blocks until a URL
// is available or all work is done.
package inmem

import (
	"context"
	"log/slog"
	"sync"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type FrontierOption func(*Frontier)

type Frontier struct {
	queues   map[string]*queue
	cooldown time.Duration
	mu       sync.Mutex
	// signal wakes up goroutines blocked in Next when a URL is added or marked done.
	// Buffered with capacity 1 to avoid blocking senders.
	signal           chan struct{}
	maxDomains       int
	maxDepth         int
	urlsGauge        metric.Int64UpDownCounter
	hostnamesCounter metric.Int64UpDownCounter
	inFlightGauge    metric.Int64UpDownCounter
	// inFlightCounter tracks URLs returned by Next that have not yet been marked done.
	// When all queues are empty and inFlightCounter is 0, Next returns ErrDone.
	inFlightCounter int
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

func WithMaxDepth(md int) FrontierOption {
	return func(f *Frontier) {
		f.maxDepth = md
	}
}

func NewFrontier(opts ...FrontierOption) *Frontier {
	meter := otel.Meter("frontier")
	urlsGauge, _ := meter.Int64UpDownCounter("crawler.frontier.urls.size")
	inFlightGauge, _ := meter.Int64UpDownCounter("crawler.frontier.inflighturls.size")
	hostnamesCounter, _ := meter.Int64UpDownCounter("crawler.frontier.hostnames.size")

	f := &Frontier{
		queues:           map[string]*queue{},
		cooldown:         time.Second,
		maxDomains:       50,
		maxDepth:         3,
		mu:               sync.Mutex{},
		signal:           make(chan struct{}, 1),
		urlsGauge:        urlsGauge,
		hostnamesCounter: hostnamesCounter,
		inFlightGauge:    inFlightGauge,
		inFlightCounter:  0,
	}

	for _, fn := range opts {
		fn(f)
	}

	slog.Info("created new Frontier", "frontier", f)

	return f
}

// AddURL enqueues a URL for crawling, creating a new per-domain queue if needed.
// Returns frontier.ErrMaxDepth if the URL exceeds the configured crawl depth,
// or frontier.ErrMaxDomainLimit if the hostname would exceed the domain cap.
func (f *Frontier) AddURL(ctx context.Context, url crawler.URL) error {
	f.mu.Lock()

	if url.Depth > f.maxDepth {
		f.mu.Unlock()
		return frontier.ErrMaxDepth
	}

	if _, ok := f.queues[url.Hostname]; !ok {
		if len(f.queues) >= f.maxDomains {
			f.mu.Unlock()
			return frontier.ErrMaxDomainLimit
		}
		f.queues[url.Hostname] = newQueue()
		f.hostnamesCounter.Add(ctx, 1)
	}

	f.queues[url.Hostname].push(url)
	f.urlsGauge.Add(ctx, 1)

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
				f.urlsGauge.Add(ctx, -1)
				f.inFlightGauge.Add(ctx, 1)
				f.inFlightCounter++
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

		// queues are empty but workers are still processing — wait for signal
		f.mu.Lock()
		if f.inFlightCounter > 0 {
			f.mu.Unlock()
			select {
			case <-f.signal:
				continue
			case <-ctx.Done():
				return crawler.URL{}, ctx.Err()
			}
		}

		f.mu.Unlock()
		// no urls left in the queues, signal work done
		return crawler.URL{}, frontier.ErrDone
	}
}

func (f *Frontier) MarkDone(ctx context.Context) error {
	f.mu.Lock()
	f.inFlightCounter--
	f.inFlightGauge.Add(ctx, -1)
	f.mu.Unlock()

	select {
	case f.signal <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	return nil
}
