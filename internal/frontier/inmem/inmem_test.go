package inmem_test

import (
	"testing"
	"testing/synctest"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier/inmem"
)

func TestFrontierAddAndRetrieveSingleURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		frontier := inmem.NewFrontier(inmem.WithCooldown(time.Second))

		url := crawler.URL{Base: "base", Path: "path", Depth: 1}
		err := frontier.AddURL(t.Context(), url)
		if err != nil {
			t.Fatalf("error adding URL to frontier. error: %v", err)
		}

		nextURL, err := frontier.Next(t.Context())
		if err != nil {
			t.Fatalf("error getting the next URL from frontier. error: %v", err)
		}

		if nextURL != url {
			t.Errorf("nextUrl and url should be the same. nextUrl: %v", nextURL)
		}
	})
}

func TestFrontierCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cooldown := time.Second
		frontier := inmem.NewFrontier(inmem.WithCooldown(cooldown))

		url1 := crawler.URL{Base: "base", Path: "path1", Depth: 1}
		url2 := crawler.URL{Base: "base", Path: "path2", Depth: 1}
		err := frontier.AddURL(t.Context(), url1)
		if err != nil {
			t.Fatalf("error adding URL1 to frontier. error: %v", err)
		}
		err = frontier.AddURL(t.Context(), url2)
		if err != nil {
			t.Fatalf("error adding URL2 to frontier. error: %v", err)
		}

		nextURL, err := frontier.Next(t.Context())
		if err != nil {
			t.Fatalf("error getting the next URL from frontier. error: %v", err)
		}

		if nextURL != url1 {
			t.Errorf("url1 should be the next url. it is %v", nextURL)
		}

		urlChan := make(chan crawler.URL, 1)

		go func() {
			nextURL2, err2 := frontier.Next(t.Context())
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
