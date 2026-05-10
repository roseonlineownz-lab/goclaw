package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/config"
)

// sttTestResponse mirrors the STT proxy JSON response for test assertions.
type sttTestResponse struct {
	Transcript string `json:"transcript"`
}

// sttEndpoint is the expected STT endpoint path.
const sttEndpoint = "/transcribe_audio"

// newChannelWithSTT is a minimal Channel stub for STT unit tests.
// It skips bot initialisation (which requires a real Telegram token).
func newChannelWithSTT(cfg config.TelegramConfig) *Channel {
	return &Channel{config: cfg}
}

// writeTempAudio writes a fake audio file and returns its path.
// The caller is responsible for removing the file after the test.
func writeTempAudio(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "stt_test_*.ogg")
	if err != nil {
		t.Fatalf("create temp audio file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp audio file: %v", err)
	}
	f.Close()
	return f.Name()
}

// --- transcribeAudio unit tests ---

// TestTranscribeAudio_NoProxy verifies that when STTProxyURL is empty, the
// function returns ("", nil) without making any HTTP call.
func TestTranscribeAudio_NoProxy(t *testing.T) {
	c := newChannelWithSTT(config.TelegramConfig{})
	transcript, err := c.transcribeAudio(context.Background(), "/any/file.ogg")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if transcript != "" {
		t.Fatalf("expected empty transcript, got: %q", transcript)
	}
}

// TestTranscribeAudio_EmptyFilePath verifies that an empty filePath is a silent
// no-op even when STT is configured.
func TestTranscribeAudio_EmptyFilePath(t *testing.T) {
	c := newChannelWithSTT(config.TelegramConfig{
		STTProxyURL: "https://stt.example.com",
	})
	transcript, err := c.transcribeAudio(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if transcript != "" {
		t.Fatalf("expected empty transcript, got: %q", transcript)
	}
}

// TestTranscribeAudio_MissingFile verifies that a non-existent file path returns
// an error (not a silent empty result).
func TestTranscribeAudio_MissingFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should never be reached.
		t.Error("unexpected HTTP call for missing file")
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	_, err := c.transcribeAudio(context.Background(), "/nonexistent/file.ogg")
	if err == nil {
		t.Fatal("expected an error for missing file, got nil")
	}
}

// TestTranscribeAudio_Success verifies the happy path: a real HTTP server returns
// {"transcript": "hello world"} and the function returns that string.
func TestTranscribeAudio_Success(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify endpoint path.
		if r.URL.Path != sttEndpoint {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Verify method.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		// Verify multipart body contains a "file" field.
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		if _, _, err := r.FormFile("file"); err != nil {
			t.Errorf("expected 'file' field in multipart form: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sttTestResponse{Transcript: "hello world"})
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	transcript, err := c.transcribeAudio(context.Background(), audioFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transcript != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", transcript)
	}
}

// TestTranscribeAudio_BearerToken verifies that STTAPIKey is sent as an
// Authorization: Bearer header.
func TestTranscribeAudio_BearerToken(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	const wantKey = "super-secret-key"
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sttTestResponse{Transcript: "ok"})
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{
		STTProxyURL: srv.URL,
		STTAPIKey:   wantKey,
	})
	if _, err := c.transcribeAudio(context.Background(), audioFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer "+wantKey {
		t.Errorf("expected Authorization %q, got %q", "Bearer "+wantKey, gotAuth)
	}
}

// TestTranscribeAudio_NoAuthHeader verifies that no Authorization header is sent
// when STTAPIKey is empty.
func TestTranscribeAudio_NoAuthHeader(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected no Authorization header, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sttTestResponse{Transcript: "ok"})
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	if _, err := c.transcribeAudio(context.Background(), audioFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTranscribeAudio_TenantID verifies that STTTenantID is forwarded as a
// multipart "tenant_id" field when set.
func TestTranscribeAudio_TenantID(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	const wantTenant = "acme-corp"
	var gotTenant string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			gotTenant = r.FormValue("tenant_id")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sttTestResponse{Transcript: "ok"})
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{
		STTProxyURL: srv.URL,
		STTTenantID: wantTenant,
	})
	if _, err := c.transcribeAudio(context.Background(), audioFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != wantTenant {
		t.Errorf("expected tenant_id %q, got %q", wantTenant, gotTenant)
	}
}

// TestTranscribeAudio_NoTenantField verifies that when STTTenantID is empty, the
// multipart form does NOT include a "tenant_id" field.
func TestTranscribeAudio_NoTenantField(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			if tid := r.FormValue("tenant_id"); tid != "" {
				t.Errorf("expected no tenant_id field, got %q", tid)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sttTestResponse{Transcript: "ok"})
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	if _, err := c.transcribeAudio(context.Background(), audioFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTranscribeAudio_UpstreamError verifies that a non-200 response is surfaced
// as an error (not silently swallowed).
func TestTranscribeAudio_UpstreamError(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	_, err := c.transcribeAudio(context.Background(), audioFile)
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to mention status 503, got: %v", err)
	}
}

// TestTranscribeAudio_InvalidJSON verifies that a 200 response with malformed
// JSON is returned as an error.
func TestTranscribeAudio_InvalidJSON(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	_, err := c.transcribeAudio(context.Background(), audioFile)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// TestTranscribeAudio_EmptyTranscript verifies that a 200 response with an empty
// transcript field returns ("", nil) — not an error.
func TestTranscribeAudio_EmptyTranscript(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sttTestResponse{Transcript: ""})
	}))
	defer srv.Close()

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	transcript, err := c.transcribeAudio(context.Background(), audioFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transcript != "" {
		t.Errorf("expected empty transcript, got %q", transcript)
	}
}

// TestTranscribeAudio_ContextCancelled verifies that a cancelled context causes
// the HTTP call to fail fast.
func TestTranscribeAudio_ContextCancelled(t *testing.T) {
	audioFile := writeTempAudio(t, "fake-ogg-bytes")
	defer os.Remove(audioFile)

	// Server that blocks until the test is done — ensures the cancel fires first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := newChannelWithSTT(config.TelegramConfig{STTProxyURL: srv.URL})
	_, err := c.transcribeAudio(ctx, audioFile)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
