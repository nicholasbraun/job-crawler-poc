package inmem_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier/inmem"
)

func TestFrontierAddAndRetrieveSingleURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inmemFrontier := inmem.NewFrontier(inmem.WithCooldown(time.Second))

		url := crawler.URL{Hostname: "base", RawURL: "path"}
		err := inmemFrontier.AddURL(t.Context(), url)
		if err != nil {
			t.Fatalf("error adding URL to frontier. error: %v", err)
		}

		url2, err := inmemFrontier.Next(t.Context())
		if err != nil {
			t.Fatalf("error getting the next URL from frontier. error: %v", err)
		}

		if url2 != url {
			t.Errorf("nextUrl and url should be the same. nextUrl: %v", url2)
		}

		err = inmemFrontier.MarkDone(t.Context(), url.RawURL)
		if err != nil {
			t.Fatalf("error marking url as done: %v", err)
		}

		_, err = inmemFrontier.Next(t.Context())
		if !errors.Is(err, frontier.ErrDone) {
			t.Fatalf("expected %v, got %v", frontier.ErrDone, err)
		}
	})
}

func TestFrontierCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cooldown := time.Second
		inmemFrontier := inmem.NewFrontier(inmem.WithCooldown(cooldown))

		url1 := crawler.URL{Hostname: "base", RawURL: "path1"}
		url2 := crawler.URL{Hostname: "base", RawURL: "path2"}
		err := inmemFrontier.AddURL(t.Context(), url1)
		if err != nil {
			t.Fatalf("error adding URL1 to frontier. error: %v", err)
		}
		err = inmemFrontier.AddURL(t.Context(), url2)
		if err != nil {
			t.Fatalf("error adding URL2 to frontier. error: %v", err)
		}

		nextURL, err := inmemFrontier.Next(t.Context())
		if err != nil {
			t.Fatalf("error getting the next URL from frontier. error: %v", err)
		}

		if nextURL != url1 {
			t.Errorf("url1 should be the next url. it is %v", nextURL)
		}

		urlChan := make(chan crawler.URL, 1)

		go func() {
			nextURL2, err2 := inmemFrontier.Next(t.Context())
			if err2 != nil {
				t.Errorf("error getting the next URL from frontier. error: %v", err2)
			}

			urlChan <- nextURL2
		}()

		synctest.Wait()

		select {
		case url := <-urlChan:
			t.Fatalf("should not have received a url before cooldown. url: %v", url)
		default:
		}

		time.Sleep(cooldown)

		synctest.Wait()

		nextURL2 := <-urlChan

		if nextURL2 != url2 {
			t.Errorf("url2 should be the next url. it is %v", nextURL2)
		}
	})
}

func TestFrontierDedup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inmemFrontier := inmem.NewFrontier(inmem.WithCooldown(time.Second))

		url := crawler.URL{Hostname: "base", RawURL: "path"}
		// Adding the same URL twice: the second is a silent no-op, so only one
		// URL should ever be popped.
		if err := inmemFrontier.AddURL(t.Context(), url); err != nil {
			t.Fatalf("error adding URL: %v", err)
		}
		if err := inmemFrontier.AddURL(t.Context(), url); err != nil {
			t.Fatalf("error adding duplicate URL: %v", err)
		}

		got, err := inmemFrontier.Next(t.Context())
		if err != nil {
			t.Fatalf("error getting next URL: %v", err)
		}
		if got != url {
			t.Errorf("expected %v, got %v", url, got)
		}

		if err := inmemFrontier.MarkDone(t.Context(), url.RawURL); err != nil {
			t.Fatalf("error marking done: %v", err)
		}

		// The duplicate must not have been enqueued: the frontier is now done.
		if _, err := inmemFrontier.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Fatalf("expected %v, got %v", frontier.ErrDone, err)
		}
	})
}
