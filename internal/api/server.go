package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/drumandbytes/momentarr/internal/cache"
	"github.com/drumandbytes/momentarr/internal/model"
)

const (
	defaultTimeout = 60 * time.Second
	maxBody        = 32 << 20
)

// Server speaks the FlareSolverr API. Requests whose host has a cached
// clearance are fetched over plain HTTP; everything else goes to the backend
// solver, at most cap(slots) at a time, so browser memory never stacks.
type Server struct {
	backend string
	slots   chan struct{}
	cache   *cache.Store
	client  *http.Client
	stats   stats
}

// stats feed the heartbeat; each is swapped to zero when reported.
type stats struct {
	hits, rejected, solves, failures, timeouts, passthrough atomic.Int64
}

func New(backend string, concurrency int, store *cache.Store) *Server {
	return &Server{
		backend: strings.TrimRight(backend, "/"),
		slots:   make(chan struct{}, concurrency),
		cache:   store,
		client:  &http.Client{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /v1", s.v1)
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	// Prowlarr's connection test looks for this msg.
	writeJSON(w, 200, model.IndexResponse{Msg: "FlareSolverr is ready!", Version: model.Version})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, model.HealthResponse{Status: model.StatusOK})
}

func (s *Server) v1(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		s.fail(w, start, 400, "read body: "+err.Error())
		return
	}
	var req model.V1Request
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Warn("rejected request with invalid json", "err", err, "remote", r.RemoteAddr)
		s.fail(w, start, 400, "invalid json: "+err.Error())
		return
	}
	log := slog.With("cmd", req.Cmd, "host", hostOf(req.URL))
	log.Debug("request received", "url", req.URL, "session", req.Session, "maxTimeout", req.MaxTimeout)
	timeout := time.Duration(req.MaxTimeout) * time.Millisecond
	if timeout < time.Second {
		timeout = defaultTimeout
	}
	// Queue wait counts against maxTimeout, same as a busy FlareSolverr.
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	cacheable := cacheableRequest(req)
	if cacheable {
		if sol, ok := s.fromCache(ctx, log, req); ok {
			s.stats.hits.Add(1)
			log.Info("served from cached clearance", "status", sol.Status, "took", since(start))
			s.succeed(w, start, sol)
			return
		}
	}

	log.Debug("waiting for a solver slot", "inUse", len(s.slots), "slots", cap(s.slots))
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		s.stats.timeouts.Add(1)
		log.Warn("timed out waiting for a solver slot", "waited", since(start), "slots", cap(s.slots))
		s.fail(w, start, 500, "Error solving the challenge. Timeout waiting for a free solver.")
		return
	}
	defer func() { <-s.slots }()
	waited := since(start)
	log.Debug("solver slot acquired", "waited", waited)

	// Whoever held the slot may have just solved this host.
	if cacheable {
		if sol, ok := s.fromCache(ctx, log, req); ok {
			s.stats.hits.Add(1)
			log.Info("served from cached clearance", "status", sol.Status, "queued", waited, "took", since(start))
			s.succeed(w, start, sol)
			return
		}
	}

	log.Debug("forwarding to backend", "backend", s.backend)
	code, out, err := s.forward(ctx, body)
	if err != nil {
		s.stats.failures.Add(1)
		log.Error("backend request failed", "backend", s.backend, "err", err, "queued", waited, "took", since(start))
		s.fail(w, start, 500, "Error solving the challenge. Backend: "+err.Error())
		return
	}
	var resp backendResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		log.Warn("backend returned unparseable json, passing it through", "code", code, "err", err)
	}
	switch {
	case !cacheable:
		s.stats.passthrough.Add(1)
		log.Info("passed through to backend", "code", code, "status", resp.Status, "took", since(start))
	case resp.Status == model.StatusOK:
		s.stats.solves.Add(1)
		log.Info("solved by backend", "message", resp.Message, "cookies", len(resp.Solution.Cookies), "queued", waited, "took", since(start))
		s.remember(log, req, resp)
	default:
		s.stats.failures.Add(1)
		log.Warn("backend could not solve", "code", code, "message", resp.Message, "queued", waited, "took", since(start))
	}
	if req.Cmd == "sessions.destroy" {
		if err := s.cache.DeleteSession(req.Session); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("could not drop session cookies", "session", req.Session, "err", err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(out)
}

func cacheableRequest(req model.V1Request) bool {
	if req.Cmd != "request.get" && req.Cmd != "request.post" || req.ReturnScreenshot {
		return false
	}
	u, err := url.Parse(req.URL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) forward(ctx context.Context, body []byte) (int, []byte, error) {
	r, err := http.NewRequestWithContext(ctx, "POST", s.backend+"/v1", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, out, err
}

// backendResponse holds only what we read from the solver: backends disagree
// on the rest (Byparr sends solution.status as a string).
type backendResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		Cookies   []model.Cookie `json:"cookies"`
		UserAgent string         `json:"userAgent"`
	} `json:"solution"`
}

func (s *Server) remember(log *slog.Logger, req model.V1Request, resp backendResponse) {
	if len(resp.Solution.Cookies) == 0 || resp.Solution.UserAgent == "" {
		log.Debug("nothing to cache: solve returned no cookies or user agent")
		return
	}
	if err := s.cache.Put(req.URL, req.Session, resp.Solution.Cookies, resp.Solution.UserAgent); err != nil {
		log.Warn("could not cache clearance; next request will solve again", "err", err)
		return
	}
	log.Debug("clearance cached", "cookies", len(resp.Solution.Cookies))
}

// fromCache replays a cached clearance over plain HTTP. cf_clearance is bound
// to IP and user agent, not to the browser, so this works until it expires;
// a challenge back means it has, and the entry is dropped.
func (s *Server) fromCache(ctx context.Context, log *slog.Logger, req model.V1Request) (*model.Solution, bool) {
	entry := s.cache.Get(req.URL, req.Session)
	if entry == nil {
		log.Debug("no cached clearance")
		return nil, false
	}
	log.Debug("trying cached clearance", "cookies", len(entry.Cookies))
	cookies := append(append([]model.Cookie{}, entry.Cookies...), req.Cookies...)
	method, body := "GET", io.Reader(nil)
	if req.Cmd == "request.post" {
		method, body = "POST", strings.NewReader(req.PostData)
	}
	r, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, false
	}
	r.Header.Set("User-Agent", entry.UserAgent)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	r.Header.Set("Accept-Language", "en-US,en;q=0.5")
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	pairs := make([]string, 0, len(cookies))
	for _, c := range cookies {
		pairs = append(pairs, c.Name+"="+c.Value)
	}
	r.Header.Set("Cookie", strings.Join(pairs, "; "))

	resp, err := s.client.Do(r)
	if err != nil {
		log.Warn("cached-clearance fetch failed, falling back to backend", "err", err)
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		log.Warn("cached-clearance fetch failed reading body, falling back to backend", "err", err)
		return nil, false
	}
	if challenged(resp, page) {
		s.stats.rejected.Add(1)
		log.Info("cached clearance rejected by site, re-solving", "status", resp.StatusCode)
		s.cache.Delete(req.URL, req.Session)
		return nil, false
	}

	for _, c := range resp.Cookies() {
		cookies = append(cookies, model.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HttpOnly})
	}
	headers := make(map[string]string, len(resp.Header))
	for name := range resp.Header {
		headers[strings.ToLower(name)] = resp.Header.Get(name)
	}
	sol := &model.Solution{
		URL:       resp.Request.URL.String(),
		Status:    resp.StatusCode,
		Headers:   headers,
		Cookies:   cookies,
		UserAgent: entry.UserAgent,
	}
	if !req.ReturnOnlyCookies {
		sol.Response = string(page)
	}
	return sol, true
}

var challengeMarkers = [][]byte{
	[]byte("<title>Just a moment...</title>"),
	[]byte("/cdn-cgi/challenge-platform/"),
	[]byte("<title>DDoS-Guard</title>"),
}

func challenged(resp *http.Response, page []byte) bool {
	if resp.Header.Get("cf-mitigated") == "challenge" {
		return true
	}
	switch resp.StatusCode {
	case 403, 429, 503:
		for _, marker := range challengeMarkers {
			if bytes.Contains(page, marker) {
				return true
			}
		}
	}
	return false
}

func (s *Server) succeed(w http.ResponseWriter, start time.Time, sol *model.Solution) {
	writeJSON(w, 200, model.V1Response{
		Status:         model.StatusOK,
		Message:        "Challenge not detected!",
		StartTimestamp: start.UnixMilli(),
		EndTimestamp:   time.Now().UnixMilli(),
		Version:        model.Version,
		Solution:       sol,
	})
}

func (s *Server) fail(w http.ResponseWriter, start time.Time, code int, msg string) {
	writeJSON(w, code, model.V1Response{
		Status:         model.StatusError,
		Message:        "Error: " + msg,
		StartTimestamp: start.UnixMilli(),
		EndTimestamp:   time.Now().UnixMilli(),
		Version:        model.Version,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Hostname()
	}
	return "-"
}

func since(start time.Time) time.Duration {
	return time.Since(start).Truncate(time.Millisecond)
}

func ListenAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8191"
}

// Heartbeat logs activity counters and backend reachability every interval,
// so a quiet log still shows the proxy is alive and whether it can solve.
func (s *Server) Heartbeat(ctx context.Context, every time.Duration) {
	s.awaitBackend(ctx)
	reachable := true
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		slog.Info("heartbeat",
			"cacheHits", s.stats.hits.Swap(0),
			"cacheRejected", s.stats.rejected.Swap(0),
			"solves", s.stats.solves.Swap(0),
			"failures", s.stats.failures.Swap(0),
			"queueTimeouts", s.stats.timeouts.Swap(0),
			"passthrough", s.stats.passthrough.Swap(0),
			"solverBusy", len(s.slots),
			"window", every)
		reachable = s.checkBackend(ctx, reachable)
	}
}

// checkBackend logs only on state changes (and the first failure), so a down
// backend isn't repeated every beat but is never silent either.
func (s *Server) checkBackend(ctx context.Context, wasReachable bool) bool {
	ok := s.probe(ctx)
	switch {
	case !ok && wasReachable:
		slog.Error("backend unreachable; uncached requests will fail", "backend", s.backend)
	case ok && !wasReachable:
		slog.Info("backend reachable again", "backend", s.backend)
	}
	return ok
}

func (s *Server) probe(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", s.backend+"/health", nil)
	if err != nil {
		slog.Error("invalid backend url", "backend", s.backend, "err", err)
		return false
	}
	resp, err := s.client.Do(r)
	if err != nil {
		slog.Debug("backend health check failed", "err", err)
		return false
	}
	_ = resp.Body.Close()
	slog.Debug("backend health check", "code", resp.StatusCode)
	return resp.StatusCode == 200
}

// awaitBackend polls until the solver answers: it usually starts slower than
// we do, so a failed first check isn't worth an error.
func (s *Server) awaitBackend(ctx context.Context) {
	started := time.Now()
	warned := false
	for !s.probe(ctx) {
		if !warned && time.Since(started) > time.Minute {
			slog.Warn("backend still not answering after 1m; uncached requests will fail", "backend", s.backend)
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	slog.Info("backend reachable", "backend", s.backend, "after", since(started))
}
