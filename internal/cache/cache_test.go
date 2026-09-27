package cache

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/drumandbytes/momentarr/internal/model"
)

func TestPersistenceAndDomainSharing(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	cookies := []model.Cookie{{Name: "clearance", Value: "value"}}
	if err := store.Put("https://example.com/one", "", cookies, "ua"); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	entry := reopened.Get("https://example.com/two", "")
	if entry == nil || len(entry.Cookies) != 1 || entry.Cookies[0].Value != "value" || entry.UserAgent != "ua" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestTTL(t *testing.T) {
	store, err := New(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("https://example.com", "", nil, "ua"); err != nil {
		t.Fatal(err)
	}
	path := store.pathFor("https://example.com", "")
	entry := Entry{SavedAt: time.Now().Add(-2 * time.Hour)}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if entry := store.Get("https://example.com", ""); entry != nil {
		t.Fatalf("expected expired entry, got %+v", entry)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected expired cache removal, got %v", err)
	}
}

func TestSessionNamespaceAndDelete(t *testing.T) {
	store, err := New(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("https://one.example", "session", []model.Cookie{{Value: "one"}}, "ua"); err != nil {
		t.Fatal(err)
	}
	if entry := store.Get("https://two.example", "session"); entry == nil || entry.Cookies[0].Value != "one" {
		t.Fatalf("session cache not shared: %+v", entry)
	}
	if entry := store.Get("https://one.example", ""); entry != nil {
		t.Fatalf("session leaked into domain cache: %+v", entry)
	}
	if err := store.Put("https://one.example", "other", []model.Cookie{{Value: "other"}}, "ua"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession("session"); err != nil {
		t.Fatal(err)
	}
	if entry := store.Get("https://one.example", "session"); entry != nil {
		t.Fatalf("deleted session remains: %+v", entry)
	}
	if entry := store.Get("https://one.example", "other"); entry == nil {
		t.Fatal("other session was deleted")
	}
}
