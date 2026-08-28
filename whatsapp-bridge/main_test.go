package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestNormalizeNumber(t *testing.T) {
	// People write numbers with punctuation; the comparison must not care.
	for _, in := range []string{"+55 11 99999-8888", "5511999998888", "+5511999998888", "(55) 11 99999 8888"} {
		if got := normalizeNumber(in); got != "5511999998888" {
			t.Errorf("normalizeNumber(%q) = %q", in, got)
		}
	}
}

func writeConf(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "instances.conf")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	original := instancesFile
	instancesFile = path
	t.Cleanup(func() { instancesFile = original })
}

func saveInstanceVars(t *testing.T) {
	t.Helper()
	name, dir, port, number, desc := instanceName, storeDir, bridgePort, expectedNumber, instanceDescription
	t.Cleanup(func() {
		instanceName, storeDir, bridgePort = name, dir, port
		expectedNumber, instanceDescription = number, desc
	})
}

func TestLoadInstance(t *testing.T) {
	writeConf(t, `
# name     port   number          description
personal   8080   15555550134     Personal US number, family and friends
work       8081   -               Work number, colleagues
minimal    8082
`)

	t.Run("full entry", func(t *testing.T) {
		saveInstanceVars(t)
		if err := loadInstance("personal"); err != nil {
			t.Fatal(err)
		}
		if instanceName != "personal" {
			t.Errorf("name = %q", instanceName)
		}
		if storeDir != "store-personal" {
			t.Errorf("store = %q", storeDir)
		}
		if bridgePort != 8080 {
			t.Errorf("port = %d", bridgePort)
		}
		if expectedNumber != "15555550134" {
			t.Errorf("number = %q", expectedNumber)
		}
		// The description runs to the end of the line, commas and all.
		if instanceDescription != "Personal US number, family and friends" {
			t.Errorf("description = %q", instanceDescription)
		}
	})

	t.Run("dash means do not check the number", func(t *testing.T) {
		saveInstanceVars(t)
		expectedNumber = ""
		if err := loadInstance("work"); err != nil {
			t.Fatal(err)
		}
		if expectedNumber != "" {
			t.Errorf("expected no number check, got %q", expectedNumber)
		}
		if instanceDescription != "Work number, colleagues" {
			t.Errorf("description = %q", instanceDescription)
		}
	})

	t.Run("name and port are enough", func(t *testing.T) {
		saveInstanceVars(t)
		if err := loadInstance("minimal"); err != nil {
			t.Fatal(err)
		}
		if storeDir != "store-minimal" || bridgePort != 8082 {
			t.Errorf("store = %q, port = %d", storeDir, bridgePort)
		}
	})

	t.Run("unknown instance lists what exists", func(t *testing.T) {
		saveInstanceVars(t)
		err := loadInstance("brasil")
		if err == nil {
			t.Fatal("expected an error")
		}
		// The message has to be actionable: a typo should show the real names.
		for _, want := range []string{"brasil", "personal", "work"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})

	t.Run("explicit environment wins", func(t *testing.T) {
		saveInstanceVars(t)
		t.Setenv("WHATSAPP_BRIDGE_PORT", "9999")
		bridgePort = 9999
		if err := loadInstance("personal"); err != nil {
			t.Fatal(err)
		}
		if bridgePort != 9999 {
			t.Errorf("env should override the conf file, got %d", bridgePort)
		}
	})
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
