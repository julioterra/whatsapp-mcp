package main

import "testing"

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
