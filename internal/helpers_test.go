package crawler_test

import "testing"

func assertStrings(t *testing.T, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("want: %s, got: %s", want, got)
	}
}
