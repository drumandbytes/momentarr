package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/drumandbytes/momentarr/internal/model"
)

type Entry struct {
	Cookies   []model.Cookie `json:"cookies"`
	UserAgent string         `json:"userAgent"`
	SavedAt   time.Time      `json:"savedAt"`
}

type Store struct {
	dir string
	ttl time.Duration
	mu  sync.Mutex
}

func New(dir string, ttlHours int) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if ttlHours <= 0 {
		ttlHours = 24
	}
	return &Store{dir: dir, ttl: time.Duration(ttlHours) * time.Hour}, nil
}

func (s *Store) pathFor(rawURL, session string) string {
	sum := sha256.Sum256([]byte(keyOf(rawURL, session)))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

func keyOf(rawURL, session string) string {
	key := session
	if key == "" {
		u, err := url.Parse(rawURL)
		key = rawURL
		if err == nil && u.Host != "" {
			key = u.Hostname()
		}
		key = "domain:" + key
	} else {
		key = "session:" + key
	}
	return key
}

func (s *Store) Get(rawURL, session string) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.pathFor(rawURL, session)
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("cookie cache unreadable", "path", path, "err", err)
		}
		return nil
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		slog.Warn("cookie cache entry corrupt, dropping", "path", path, "err", err)
		_ = os.Remove(path)
		return nil
	}
	if time.Since(e.SavedAt) > s.ttl || expired(e.Cookies) {
		slog.Debug("cookie cache entry expired", "key", keyOf(rawURL, session), "saved", e.SavedAt)
		_ = os.Remove(path)
		return nil
	}
	return &e
}

// expired reports whether the site's own cf_clearance expiry has passed, which
// is usually well before COOKIE_TTL_HOURS.
func expired(cookies []model.Cookie) bool {
	now := float64(time.Now().Unix())
	for _, c := range cookies {
		if c.Name == "cf_clearance" && c.Expires > 0 && c.Expires < now {
			return true
		}
	}
	return false
}

func (s *Store) Delete(rawURL, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(s.pathFor(rawURL, session))
}

func (s *Store) Put(rawURL, session string, cookies []model.Cookie, ua string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(Entry{Cookies: cookies, UserAgent: ua, SavedAt: time.Now()})
	if err != nil {
		return err
	}
	return os.WriteFile(s.pathFor(rawURL, session), b, 0o600)
}

func (s *Store) DeleteSession(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(s.pathFor("", session))
}
