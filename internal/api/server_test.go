package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drumandbytes/momentarr/internal/cache"
	"github.com/drumandbytes/momentarr/internal/model"
)

// origin serves the page only to the cf_clearance value in valid, sent with
// the solver's user agent.
func origin(t *testing.T, valid *atomic.Value) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("cf_clearance")
		if err != nil || c.Value != valid.Load().(string) || r.UserAgent() != "solver-ua" {
			w.WriteHeader(403)
			_, _ = fmt.Fprint(w, "<html><title>Just a moment...</title></html>")
			return
		}
		_, _ = fmt.Fprint(w, "<html><title>real page</title></html>")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// backend mimics Byparr (solution.status is a string) and hands out the
// current valid clearance after delay.
func backend(t *testing.T, valid *atomic.Value, calls *atomic.Int32, delay time.Duration) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(delay)
		_, _ = fmt.Fprintf(w, `{"status":"ok","message":"Success","solution":{"status":"200","response":"<html>solved</html>","userAgent":"solver-ua","cookies":[{"name":"cf_clearance","value":%q,"domain":"x","expires":%d}]}}`,
			valid.Load().(string), time.Now().Add(time.Hour).Unix())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T, delay time.Duration) (*httptest.Server, *atomic.Value, *atomic.Int32, string) {
	var valid atomic.Value
	valid.Store("first")
	var calls atomic.Int32
	site := origin(t, &valid)
	store, err := cache.New(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(backend(t, &valid, &calls, delay).URL, 1, store).Handler())
	t.Cleanup(srv.Close)
	return srv, &valid, &calls, site.URL
}

func solve(t *testing.T, srv *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1", "application/json", strings.NewReader(body))
	if err != nil {
		t.Error(err)
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Error(err)
	}
	return resp.StatusCode, out
}

func get(u string, timeoutMs int) string {
	return fmt.Sprintf(`{"cmd":"request.get","url":%q,"maxTimeout":%d}`, u, timeoutMs)
}

func response(out map[string]any) string {
	sol, _ := out["solution"].(map[string]any)
	s, _ := sol["response"].(string)
	return s
}

func TestCachedClearanceSkipsBackend(t *testing.T) {
	srv, _, calls, site := setup(t, 0)
	if _, out := solve(t, srv, get(site, 5000)); response(out) != "<html>solved</html>" {
		t.Fatalf("first request not passed through from backend: %v", out)
	}
	code, out := solve(t, srv, get(site+"/other", 5000))
	if code != 200 || out["status"] != "ok" || !strings.Contains(response(out), "real page") {
		t.Fatalf("cached request: %d %v", code, out)
	}
	if calls.Load() != 1 {
		t.Fatalf("backend calls = %d, want 1", calls.Load())
	}
}

func TestRejectedClearanceIsResolved(t *testing.T) {
	srv, valid, calls, site := setup(t, 0)
	solve(t, srv, get(site, 5000))
	valid.Store("second") // the site revoked the clearance
	if _, out := solve(t, srv, get(site, 5000)); response(out) != "<html>solved</html>" {
		t.Fatalf("rejected clearance not re-solved: %v", out)
	}
	if _, out := solve(t, srv, get(site, 5000)); !strings.Contains(response(out), "real page") {
		t.Fatalf("new clearance not cached: %v", out)
	}
	if calls.Load() != 2 {
		t.Fatalf("backend calls = %d, want 2", calls.Load())
	}
}

func TestQueuedBurstSolvesOnce(t *testing.T) {
	srv, _, calls, site := setup(t, 200*time.Millisecond)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if code, out := solve(t, srv, get(site, 5000)); code != 200 {
				t.Errorf("burst request: %d %v", code, out)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("backend calls = %d, want 1 for a burst on one host", calls.Load())
	}
}

func TestQueueWaitCountsAgainstTimeout(t *testing.T) {
	srv, _, _, site := setup(t, 1500*time.Millisecond)
	done := make(chan struct{})
	go func() {
		defer close(done)
		solve(t, srv, get(site, 5000))
	}()
	time.Sleep(100 * time.Millisecond)
	code, out := solve(t, srv, get(site+"/b", 1000))
	if code != 500 || !strings.Contains(fmt.Sprint(out["message"]), "Timeout waiting for a free solver") {
		t.Fatalf("got %d %v", code, out)
	}
	<-done
}

func TestNonRequestCommandsPassThrough(t *testing.T) {
	srv, _, calls, _ := setup(t, 0)
	solve(t, srv, `{"cmd":"sessions.list"}`)
	solve(t, srv, `{"cmd":"request.get","url":"not a url"}`)
	if calls.Load() != 2 {
		t.Fatalf("backend calls = %d, want 2", calls.Load())
	}
}

func TestIndexKeepsFlareSolverrMessage(t *testing.T) {
	srv := httptest.NewServer(New("http://unused", 1, nil).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out model.IndexResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Msg != "FlareSolverr is ready!" {
		t.Fatalf("msg = %q", out.Msg)
	}
}
