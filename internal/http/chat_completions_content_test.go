package http

import (
	"encoding/json"
	"testing"
)

func TestFlexContentUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"plain string", `"hello world"`, "hello world"},
		{"empty string", `""`, ""},
		{"null", `null`, ""},
		{"single text part", `[{"type":"text","text":"hi"}]`, "hi"},
		{"multiple text parts", `[{"type":"text","text":"one"},{"type":"text","text":"two"}]`, "one\ntwo"},
		{"mixed parts drops non-text", `[{"type":"text","text":"keep"},{"type":"image_url","image_url":{"url":"x"}}]`, "keep"},
		{"empty array", `[]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c flexContent
			if err := json.Unmarshal([]byte(tc.json), &c); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.json, err)
			}
			if string(c) != tc.want {
				t.Fatalf("got %q, want %q", string(c), tc.want)
			}
		})
	}
}

func TestFlexContentMarshalRoundTrip(t *testing.T) {
	original := flexContent("roundtrip text")
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `"roundtrip text"` {
		t.Fatalf("got %s, want \"roundtrip text\"", data)
	}
}

func TestChatMessageWithContentArray(t *testing.T) {
	// The exact failure mode from openclaw: Anthropic-style content blocks.
	body := `{"role":"user","content":[{"type":"text","text":"hello from claude"}]}`
	var msg chatMessage
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Role != "user" {
		t.Fatalf("role: got %q want user", msg.Role)
	}
	if string(msg.Content) != "hello from claude" {
		t.Fatalf("content: got %q", string(msg.Content))
	}
}
