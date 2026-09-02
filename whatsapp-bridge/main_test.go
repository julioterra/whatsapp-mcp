package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
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
# name, port, number, "description"
personal, 8080, 15555550134, "Personal US number, family and friends"
work,     8081, -,           "Work number, colleagues"
minimal,  8082
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

	t.Run("a missing number column is not mistaken for one", func(t *testing.T) {
		// "personal 8080 My US number, family" -- the whole tail is the
		// description, and no number check is armed.
		writeConf(t, "nonumber, 8083, \"My US number, family and friends\"\n")
		saveInstanceVars(t)
		expectedNumber = ""
		if err := loadInstance("nonumber"); err != nil {
			t.Fatal(err)
		}
		if expectedNumber != "" {
			t.Errorf("should not have taken a number, got %q", expectedNumber)
		}
		if instanceDescription != "My US number, family and friends" {
			t.Errorf("description = %q", instanceDescription)
		}
	})

	t.Run("spacing around commas is ignored and quotes are optional", func(t *testing.T) {
		writeConf(t, "loose,8084,15551234567,Unquoted description\n")
		saveInstanceVars(t)
		if err := loadInstance("loose"); err != nil {
			t.Fatal(err)
		}
		if bridgePort != 8084 || expectedNumber != "15551234567" {
			t.Errorf("port = %d, number = %q", bridgePort, expectedNumber)
		}
		if instanceDescription != "Unquoted description" {
			t.Errorf("description = %q", instanceDescription)
		}
	})

	t.Run("an empty number field skips the check", func(t *testing.T) {
		writeConf(t, "blank, 8085, , \"Some account\"\n")
		saveInstanceVars(t)
		expectedNumber = ""
		if err := loadInstance("blank"); err != nil {
			t.Fatal(err)
		}
		if expectedNumber != "" {
			t.Errorf("expected no check, got %q", expectedNumber)
		}
		if instanceDescription != "Some account" {
			t.Errorf("description = %q", instanceDescription)
		}
	})

	t.Run("a bad port says so in plain words", func(t *testing.T) {
		writeConf(t, "oops, eighty-eighty, -, \"Typo\"\n")
		saveInstanceVars(t)
		err := loadInstance("oops")
		if err == nil || !strings.Contains(err.Error(), "eighty-eighty") {
			t.Errorf("error should quote the bad value, got %v", err)
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

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	// A path set in a JSON config or a settings file never goes through a
	// shell, so the tilde arrives literally.
	if got, want := expandHome("~/Claude/whatsapp"), filepath.Join(home, "Claude/whatsapp"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := expandHome("/tmp/absolute"); got != "/tmp/absolute" {
		t.Errorf("absolute path should be untouched, got %q", got)
	}
	if got := expandHome(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
	// Another user's home is left alone here and rejected by checkMediaDir,
	// rather than half-imitating what a shell does.
	if got := expandHome("~root/Documents"); got != "~root/Documents" {
		t.Errorf("got %q", got)
	}
}

func TestCheckMediaDir(t *testing.T) {
	original := mediaDir
	defer func() { mediaDir = original }()

	for _, ok := range []string{"", "/Users/someone/Claude", "/Users/someone/5. Claude/Downloads"} {
		mediaDir = ok
		if err := checkMediaDir(); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}

	// A shell resolves this; nothing here can, and creating a folder called
	// "~root" would put downloads somewhere nobody would look.
	mediaDir = "~root/Documents"
	err := checkMediaDir()
	if err == nil {
		t.Fatal("expected ~otheruser to be rejected")
	}
	if !strings.Contains(err.Error(), "full") {
		t.Errorf("error should say to write the full path, got %v", err)
	}
}

func TestSettingLine(t *testing.T) {
	tests := []struct {
		line    string
		key     string
		value   string
		setting bool
	}{
		{`media_dir = /Users/me/Claude`, "media_dir", "/Users/me/Claude", true},
		{`media_dir = "/Users/me/5. Claude/Downloads"`, "media_dir", "/Users/me/5. Claude/Downloads", true},
		// A comma in the folder name must not turn this into an account line.
		{`media_dir = /Users/me/Photos, scans/WhatsApp`, "media_dir", "/Users/me/Photos, scans/WhatsApp", true},
		{`personal, 8080, -, "Mine"`, "", "", false},
		{`# a comment`, "", "", false},
		{``, "", "", false},
	}

	for _, tt := range tests {
		key, value, ok := settingLine(tt.line)
		if ok != tt.setting || key != tt.key || value != tt.value {
			t.Errorf("settingLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.line, key, value, ok, tt.key, tt.value, tt.setting)
		}
	}
}

// A settings line must not be offered as an account name when someone mistypes.
func TestSettingsAreNotAccounts(t *testing.T) {
	writeConf(t, "media_dir = /tmp/media\npersonal, 8080, -, \"Mine\"\n")
	saveInstanceVars(t)

	err := loadInstance("typo")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "media_dir") {
		t.Errorf("settings line listed as an account: %v", err)
	}
	if !strings.Contains(err.Error(), "personal") {
		t.Errorf("real account missing from %v", err)
	}
}

func TestMediaPath(t *testing.T) {
	originalDir, originalName := mediaDir, instanceName
	defer func() { mediaDir, instanceName = originalDir, originalName }()

	instanceName = "personal"

	t.Run("unset keeps attachments in the store", func(t *testing.T) {
		mediaDir = ""
		storeDir = "store-personal"
		if got, want := mediaPath("alice@s.whatsapp.net"),
			filepath.Join("store-personal", "alice@s.whatsapp.net"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("configured directory is used", func(t *testing.T) {
		mediaDir = "/Users/someone/Claude/whatsapp"
		if got, want := mediaPath("alice@s.whatsapp.net"),
			"/Users/someone/Claude/whatsapp/personal/alice@s.whatsapp.net"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// A direct chat is named after the other person, so the same JID appears
	// on every account. Without the account in the path, two accounts sharing
	// a media directory would overwrite each other's copy of the same file.
	t.Run("accounts do not collide in a shared directory", func(t *testing.T) {
		mediaDir = "/shared"
		instanceName = "personal"
		personal := mediaPath("alice@s.whatsapp.net")
		instanceName = "work"
		work := mediaPath("alice@s.whatsapp.net")
		if personal == work {
			t.Errorf("both accounts resolved to %q", personal)
		}
	})
}

func TestLoadSettingsMediaDir(t *testing.T) {
	original := mediaDir
	defer func() { mediaDir = original }()

	writeConf(t, `
# a settings line has no comma; account lines always do
media_dir = /tmp/claude-media

personal, 8080, 15555550134, "Personal, with a comma"
`)

	mediaDir = ""
	loadSettings()
	if mediaDir != "/tmp/claude-media" {
		t.Errorf("media_dir = %q", mediaDir)
	}

	// The account line must not be mistaken for a setting, nor vice versa.
	saveInstanceVars(t)
	if err := loadInstance("personal"); err != nil {
		t.Fatal(err)
	}
	if instanceDescription != "Personal, with a comma" {
		t.Errorf("description = %q", instanceDescription)
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

// History sync and on-demand backfill both arrive as batches of old messages.
// Neither may drag a chat backwards in the list_chats ordering.
func TestStoreChatKeepsLatestTimestamp(t *testing.T) {
	original := storeDir
	defer func() { storeDir = original }()
	storeDir = filepath.Join(t.TempDir(), "store-test")

	store, err := NewMessageStore()
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	defer store.Close()

	const jid = "120363000000000001@g.us"
	const name = "Test Group"
	recent := time.Date(2026, 8, 28, 11, 3, 27, 0, time.UTC)
	backfilled := time.Date(2026, 5, 28, 11, 45, 55, 0, time.UTC)

	if err := store.StoreChat(jid, name, recent); err != nil {
		t.Fatalf("storing the recent message: %v", err)
	}
	if err := store.StoreChat(jid, name, backfilled); err != nil {
		t.Fatalf("storing the backfilled message: %v", err)
	}

	if got := storedChatTime(t, store, jid); !got.Equal(recent) {
		t.Errorf("a backfilled message moved the chat backwards: got %s, want %s", got, recent)
	}

	// A genuinely newer message still moves it forward.
	newer := recent.Add(time.Hour)
	if err := store.StoreChat(jid, name, newer); err != nil {
		t.Fatalf("storing the newer message: %v", err)
	}
	if got := storedChatTime(t, store, jid); !got.Equal(newer) {
		t.Errorf("a newer message did not move the chat forward: got %s, want %s", got, newer)
	}
}

func storedChatTime(t *testing.T, store *MessageStore, jid string) time.Time {
	t.Helper()
	var epoch int64
	err := store.db.QueryRow("SELECT strftime('%s', last_message_time) FROM chats WHERE jid = ?", jid).Scan(&epoch)
	if err != nil {
		t.Fatalf("reading last_message_time: %v", err)
	}
	return time.Unix(epoch, 0).UTC()
}

// A message the bridge cannot store has to say what it was. Without this the
// only trace of a dropped message is its absence, which is indistinguishable
// from a message that never arrived at all.
func TestMessageTypeName(t *testing.T) {
	tests := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{"nil", nil, "nil"},
		{"empty", &waProto.Message{}, "empty"},
		{"plain text", &waProto.Message{Conversation: proto.String("bom dia")}, "conversation"},
		{"reaction", &waProto.Message{ReactionMessage: &waProto.ReactionMessage{}}, "reactionMessage"},
		// The shape that cost this install a message: from history sync an
		// edit arrives as a protocolMessage carrying the new body, and neither
		// extractor looks inside one.
		{"edit", &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{}}, "protocolMessage"},
	}

	for _, tt := range tests {
		if got := messageTypeName(tt.msg); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// messageContextInfo rides along on nearly every message and would bury the
// field that actually says what the message was.
func TestMessageTypeNameIgnoresContextInfo(t *testing.T) {
	msg := &waProto.Message{
		Conversation:       proto.String("bom dia"),
		MessageContextInfo: &waProto.MessageContextInfo{},
	}
	if got, want := messageTypeName(msg), "conversation"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The bug this guards against: from history sync an edited message arrives as
// a bare protocolMessage carrying the new body under the *original* message's
// ID. Reading fields straight off the WebMessageInfo — as handleHistorySync
// used to — finds no text and drops it silently, which is how this install
// lost a message it had already stored correctly from the live path.
func TestHistorySyncEditIsStorable(t *testing.T) {
	client := testClient(t)

	const originalID = "3BAAAAAAAAAAAAAAAAAA"
	const editID = "3BFFFFFFFFFFFFFFFFFF"
	const body = "Mensagem de teste"
	chatJID := types.JID{User: "120363000000000001", Server: types.GroupServer}

	webMsg := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			RemoteJID: proto.String(chatJID.String()),
			FromMe:    proto.Bool(true),
			ID:        proto.String(editID),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 8, 28, 10, 19, 11, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Key:           &waProto.MessageKey{ID: proto.String(originalID)},
				Type:          waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				EditedMessage: &waProto.Message{Conversation: proto.String(body)},
			},
		},
	}

	// What the old code did. It finds nothing, and nothing is what got stored.
	if got := extractTextContent(webMsg.GetMessage()); got != "" {
		t.Fatalf("premise of this test is wrong: raw extraction found %q", got)
	}

	evt, err := client.ParseWebMessage(chatJID, webMsg)
	if err != nil {
		t.Fatalf("parsing the history message: %v", err)
	}

	if got := extractTextContent(evt.Message); got != body {
		t.Errorf("edited body: got %q, want %q", got, body)
	}
	// The edit has to land on the message it edits, not add a second row.
	if evt.Info.ID != originalID {
		t.Errorf("edit filed under %q, want the original %q", evt.Info.ID, originalID)
	}
}

// The message this install actually lost was 584 characters over six lines,
// which WhatsApp carries as an extendedTextMessage rather than a conversation.
// Same protocolMessage wrapper, different field inside it.
func TestHistorySyncEditOfLongTextIsStorable(t *testing.T) {
	client := testClient(t)

	const originalID = "3BAAAAAAAAAAAAAAAAAA"
	body := "Mensagem de teste:\n\n1. Primeiro item\n2. Segundo item"
	chatJID := types.JID{User: "120363000000000001", Server: types.GroupServer}

	webMsg := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			RemoteJID: proto.String(chatJID.String()),
			FromMe:    proto.Bool(true),
			ID:        proto.String("3BFFFFFFFFFFFFFFFFFF"),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 8, 28, 10, 19, 11, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Key:  &waProto.MessageKey{ID: proto.String(originalID)},
				Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				EditedMessage: &waProto.Message{
					ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String(body)},
				},
			},
		},
	}

	if got := extractTextContent(webMsg.GetMessage()); got != "" {
		t.Fatalf("premise of this test is wrong: raw extraction found %q", got)
	}

	evt, err := client.ParseWebMessage(chatJID, webMsg)
	if err != nil {
		t.Fatalf("parsing the history message: %v", err)
	}

	if got := extractTextContent(evt.Message); got != body {
		t.Errorf("edited body: got %q, want %q", got, body)
	}
	if evt.Info.ID != originalID {
		t.Errorf("edit filed under %q, want the original %q", evt.Info.ID, originalID)
	}
}

// A client with an identity but no connection. ParseWebMessage only needs the
// former: it resolves "from me" against the linked account.
func testClient(t *testing.T) *whatsmeow.Client {
	t.Helper()

	dsn := "file:" + filepath.Join(t.TempDir(), "whatsapp.db") + "?_foreign_keys=on"
	container, err := sqlstore.New(context.Background(), "sqlite3", dsn, waLog.Noop)
	if err != nil {
		t.Fatalf("creating the device store: %v", err)
	}
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550001111", Server: types.DefaultUserServer}

	return whatsmeow.NewClient(device, waLog.Noop)
}

// messages holds a foreign key to chats(jid), so a message inserted before its
// chat is rejected outright. On a fresh store — which is exactly what a
// history sync writes into — that is every message in the batch, and the only
// symptom is a wall of "FOREIGN KEY constraint failed" warnings.
func TestStoreConversationCreatesChatFirst(t *testing.T) {
	original := storeDir
	defer func() { storeDir = original }()
	storeDir = filepath.Join(t.TempDir(), "store-test")

	store, err := NewMessageStore()
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	defer store.Close()

	const chatJID = "100000000000001@lid"
	msgs := []*events.Message{
		historyMessage(t, chatJID, "D7BBBBBBBBBBBBB53C", "bom dia", time.Date(2026, 8, 28, 10, 18, 51, 0, time.UTC)),
		historyMessage(t, chatJID, "D7BBBBBBBBBBBBB53D", "tudo bem?", time.Date(2026, 8, 28, 10, 19, 11, 0, time.UTC)),
	}

	stored, skipped := storeConversation(store, chatJID, "Test", msgs, waLog.Noop)
	if stored != len(msgs) || skipped != 0 {
		t.Fatalf("stored %d and skipped %d, want %d and 0", stored, skipped, len(msgs))
	}

	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM messages WHERE chat_jid = ?", chatJID).Scan(&count); err != nil {
		t.Fatalf("counting messages: %v", err)
	}
	if count != len(msgs) {
		t.Errorf("stored message count: got %d, want %d", count, len(msgs))
	}

	// The chat carries the newest message in the batch, not the first one it
	// happened to see.
	if got, want := storedChatTime(t, store, chatJID), msgs[1].Info.Timestamp; !got.Equal(want) {
		t.Errorf("chat timestamp: got %s, want %s", got, want)
	}
}

func historyMessage(t *testing.T, chatJID, id, body string, ts time.Time) *events.Message {
	t.Helper()

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		t.Fatalf("parsing %s: %v", chatJID, err)
	}

	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: jid, Sender: jid},
			ID:            id,
			Timestamp:     ts,
		},
		Message: &waProto.Message{Conversation: proto.String(body)},
	}
}

// Two attachments in the same chat used to resolve to one path: generated
// names have one-second resolution and a document carries whatever name the
// sender chose. downloadMedia returns an existing file without fetching, so
// the second message handed back the first one's bytes.
func TestMediaFileNameIsUniquePerMessage(t *testing.T) {
	first := mediaFileName("3EB0AAAAAAAAAAAAAAAA", "image_20260901_135344.jpg")
	second := mediaFileName("3EB0BBBBBBBBBBBBBBBB", "image_20260901_135344.jpg")
	if first == second {
		t.Errorf("both messages resolved to %q", first)
	}

	// Same for two people sending a file with the same name, however far
	// apart they sent it.
	one := mediaFileName("3EB0AAAAAAAAAAAAAAAA", "Contract.pdf")
	two := mediaFileName("3EB0BBBBBBBBBBBBBBBB", "Contract.pdf")
	if one == two {
		t.Errorf("both documents resolved to %q", one)
	}
}

// The point of keeping the sender's filename is that a person can still tell
// what the file is. Real documents have spaces, commas and accents.
func TestMediaFileNameKeepsSenderName(t *testing.T) {
	const sent = "Relatório Final, versão 2.pdf"
	got := mediaFileName("3EB0AAAAAAAAAAAAAAAA", sent)
	if !strings.HasSuffix(got, sent) {
		t.Errorf("got %q, want it to end in %q", got, sent)
	}
	if !strings.HasSuffix(got, ".pdf") {
		t.Errorf("got %q, want the extension preserved", got)
	}
}

// A document filename arrives from the network, and a sender is free to name
// a file anything at all.
func TestMediaFileNameStaysInTheChatDirectory(t *testing.T) {
	for _, name := range []string{
		"../../../../Library/LaunchAgents/x.plist",
		"/etc/passwd",
		"..",
		".",
		"",
		`..\..\windows\system32\evil.dll`,
	} {
		got := mediaFileName("3EB0AAAAAAAAAAAAAAAA", name)
		if strings.ContainsRune(got, filepath.Separator) {
			t.Errorf("%q produced %q, which is more than one path element", name, got)
		}
		if dir := filepath.Dir(filepath.Join("/chat", got)); dir != "/chat" {
			t.Errorf("%q escaped to %q", name, dir)
		}
	}
}

// A message ID is not sender-controlled the way a document name is, but it is
// still concatenated into a path.
func TestMediaFileNameSanitizesTheMessageID(t *testing.T) {
	got := mediaFileName("../../evil", "image_20260901_135344.jpg")
	if strings.ContainsRune(got, filepath.Separator) {
		t.Errorf("got %q, which is more than one path element", got)
	}
}

// History sync delivers months of messages in one burst. Naming attachments
// from the clock at that moment dates every one of them to the sync.
func TestExtractMediaInfoNamesFromSendTime(t *testing.T) {
	sent := time.Date(2026, 6, 28, 10, 15, 0, 0, time.UTC)
	msg := &waProto.Message{ImageMessage: &waProto.ImageMessage{
		URL: proto.String("https://mmg.whatsapp.net/d/f/AAAA.enc"),
	}}

	mediaType, filename, _, _, _, _, _ := extractMediaInfo(msg, sent)
	if mediaType != "image" {
		t.Fatalf("media type: got %q, want %q", mediaType, "image")
	}
	if got, want := filename, "image_20260628_101500.jpg"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A document keeps the name it was sent with; only a nameless one is
// generated, and then from the send time too.
func TestExtractMediaInfoKeepsDocumentName(t *testing.T) {
	sent := time.Date(2026, 6, 28, 10, 15, 0, 0, time.UTC)

	named := &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
		FileName: proto.String("Contract.pdf"),
	}}
	if _, got, _, _, _, _, _ := extractMediaInfo(named, sent); got != "Contract.pdf" {
		t.Errorf("got %q, want %q", got, "Contract.pdf")
	}

	nameless := &waProto.Message{DocumentMessage: &waProto.DocumentMessage{}}
	if _, got, _, _, _, _, _ := extractMediaInfo(nameless, sent); got != "document_20260628_101500" {
		t.Errorf("got %q, want %q", got, "document_20260628_101500")
	}
}

// The bug as it was reported: two images in one chat, asked for one after the
// other, came back as the same picture. downloadMedia returns an existing file
// without fetching anything, and both messages pointed at the same path.
func TestDownloadMediaDoesNotServeAnotherMessagesFile(t *testing.T) {
	originalStore, originalMedia, originalName := storeDir, mediaDir, instanceName
	defer func() { storeDir, mediaDir, instanceName = originalStore, originalMedia, originalName }()
	storeDir = filepath.Join(t.TempDir(), "store-test")
	mediaDir, instanceName = "", "test"

	store, err := NewMessageStore()
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	defer store.Close()

	const chatJID = "100000000000001@lid"
	const shared = "image_20260901_135344.jpg"
	const firstID, secondID = "3EB0AAAAAAAAAAAAAAAA", "3EB0BBBBBBBBBBBBBBBB"
	sent := time.Date(2026, 9, 1, 13, 53, 44, 0, time.UTC)

	if err := store.StoreChat(chatJID, "Test", sent); err != nil {
		t.Fatalf("storing the chat: %v", err)
	}
	// Both rows carry the same filename, which is what history sync produces:
	// the name has one-second resolution and these were sent in one second.
	for _, id := range []string{firstID, secondID} {
		if err := store.StoreMessage(id, chatJID, "someone", "", sent, false,
			"image", shared, "", nil, nil, nil, 0); err != nil {
			t.Fatalf("storing %s: %v", id, err)
		}
	}

	// The first image has already been downloaded.
	chatDir := mediaPath(chatJID)
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		t.Fatalf("creating the chat directory: %v", err)
	}
	firstPath := filepath.Join(chatDir, mediaFileName(firstID, shared))
	if err := os.WriteFile(firstPath, []byte("first image"), 0644); err != nil {
		t.Fatalf("writing the first image: %v", err)
	}

	// Asking for it again is a cache hit, and still the right file.
	ok, _, _, path, err := downloadMedia(nil, store, firstID, chatJID)
	if err != nil || !ok {
		t.Fatalf("first image: ok=%v err=%v", ok, err)
	}
	if got, want := filepath.Base(path), filepath.Base(firstPath); got != want {
		t.Errorf("first image resolved to %q, want %q", got, want)
	}

	// The second must not be answered with the first one's file. These rows
	// carry no URL, so the only honest outcomes are a download attempt or a
	// failure — never a silent success pointing at the first image.
	ok, _, _, path, err = downloadMedia(nil, store, secondID, chatJID)
	if ok && path == firstPath {
		t.Fatalf("second image was served the first image's file at %s", path)
	}
	if err == nil {
		t.Errorf("second image: got success, want a download attempt to fail on the missing URL")
	}
}
