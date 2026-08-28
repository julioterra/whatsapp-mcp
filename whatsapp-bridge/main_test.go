package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnvOr(t *testing.T) {
	const key = "WHATSAPP_TEST_STRING"

	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("unset should use the fallback, got %q", got)
	}

	t.Setenv(key, "configured")
	if got := envOr(key, "fallback"); got != "configured" {
		t.Errorf("set should win, got %q", got)
	}

	// An empty value is treated as unset, so exporting an empty variable
	// cannot silently point the bridge at the filesystem root.
	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("empty should use the fallback, got %q", got)
	}
}

func TestEnvIntOr(t *testing.T) {
	const key = "WHATSAPP_TEST_PORT"

	if got := envIntOr(key, 8080); got != 8080 {
		t.Errorf("unset should use the fallback, got %d", got)
	}

	t.Setenv(key, "8081")
	if got := envIntOr(key, 8080); got != 8081 {
		t.Errorf("set should win, got %d", got)
	}

	// A typo must not stop the bridge from starting.
	t.Setenv(key, "eighty-eighty")
	if got := envIntOr(key, 8080); got != 8080 {
		t.Errorf("unparseable should fall back, got %d", got)
	}
}

// Guards the actual point of the store setting: two instances must end up
// with two separate databases, not one shared one.
func TestNewMessageStoreUsesConfiguredDir(t *testing.T) {
	original := storeDir
	defer func() { storeDir = original }()

	for _, name := range []string{"us", "brasil"} {
		storeDir = filepath.Join(t.TempDir(), "store-"+name)

		store, err := NewMessageStore()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if err := store.StoreChat("test@s.whatsapp.net", name, time.Now()); err != nil {
			t.Fatalf("%s: storing a chat: %v", name, err)
		}
		store.Close()

		if _, err := os.Stat(filepath.Join(storeDir, "messages.db")); err != nil {
			t.Errorf("%s: messages.db not created in %s: %v", name, storeDir, err)
		}
	}
}

func TestStorePath(t *testing.T) {
	original := storeDir
	defer func() { storeDir = original }()

	storeDir = "store-brasil"
	if got, want := storePath("messages.db"), filepath.Join("store-brasil", "messages.db"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	storeDir = "/tmp/whatsapp-us"
	if got, want := storePath("media", "chat"), "/tmp/whatsapp-us/media/chat"; got != want {
		t.Errorf("absolute store dir: got %q, want %q", got, want)
	}
}

// The CDN rejects a direct path that has lost its query string, because
// ccb/oh/oe are the signature. whatsmeow appends "&hash=..." to whatever we
// return, so the "?" has to survive.
func TestExtractDirectPathFromURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "signed url keeps its query",
			in:   "https://mmg.whatsapp.net/v/t62.7117-24/710055220_1420887239942518_8317036355672679523_n.enc?ccb=11-4&oh=01_Q5Aa&oe=68B0C0DE&_nc_sid=5e03e0",
			want: "/v/t62.7117-24/710055220_1420887239942518_8317036355672679523_n.enc?ccb=11-4&oh=01_Q5Aa&oe=68B0C0DE&_nc_sid=5e03e0",
		},
		{
			name: "url without a query is unchanged apart from the host",
			in:   "https://mmg.whatsapp.net/v/t62.7118-24/13812002_698058036224062_n.enc",
			want: "/v/t62.7118-24/13812002_698058036224062_n.enc",
		},
		{
			name: "host other than mmg.whatsapp.net still yields a path",
			in:   "https://media-lhr8-2.cdn.whatsapp.net/v/t62.7114-24/file.enc?ccb=11-4&oh=abc",
			want: "/v/t62.7114-24/file.enc?ccb=11-4&oh=abc",
		},
		{
			name: "unparseable input falls back to the original string",
			in:   "not a url",
			want: "not a url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractDirectPathFromURL(tt.in); got != tt.want {
				t.Errorf("extractDirectPathFromURL(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// whatsmeow rejects a direct path that does not start with a slash, so guard
// that separately from the exact-output cases above.
func TestExtractDirectPathStartsWithSlash(t *testing.T) {
	got := extractDirectPathFromURL("https://mmg.whatsapp.net/v/t62.7117-24/file.enc?ccb=11-4")
	if got[0] != '/' {
		t.Errorf("direct path must start with a slash, got %q", got)
	}
}
