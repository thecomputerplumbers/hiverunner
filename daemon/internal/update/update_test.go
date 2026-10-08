package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNewerComparesReleasesOnly(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.3.3", "0.3.4", true},
		{"0.3.4", "0.3.4", false},
		{"0.4.0", "0.3.9", false},
		{"0.9.9", "1.0.0", true},
		{"v0.3.3", "0.3.4", true},
		// A build by hand is never behind, and nonsense is not a release.
		{"dev", "0.3.4", false},
		{"", "0.3.4", false},
		{"0.3.3", "latest", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestCheckRemembersTheETag(t *testing.T) {
	t.Setenv("HIVE_CONFIG", filepath.Join(t.TempDir(), "machine.json"))
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/latest.json" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"version":"0.4.0","commit":"abc","image":{"version":"7-2.337.0"}}`))
	}))
	defer server.Close()
	t.Setenv("HIVE_RELEASES", server.URL)

	state, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	latest, changed, err := Check(context.Background(), &state)
	if err != nil || !changed || latest.Version != "0.4.0" || latest.Image.Version != "7-2.337.0" {
		t.Fatalf("first check: %+v %v %v", latest, changed, err)
	}
	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	again, err := LoadState()
	if err != nil || again.ETag != `"v1"` || again.Latest != "0.4.0" {
		t.Fatalf("state after save: %+v %v", again, err)
	}
	latest, changed, err = Check(context.Background(), &again)
	if err != nil || changed || latest.Version != "0.4.0" {
		t.Fatalf("second check: %+v %v %v", latest, changed, err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d", hits)
	}
}
