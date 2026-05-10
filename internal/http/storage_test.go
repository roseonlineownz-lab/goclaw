package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStorageTestFile creates a file with the given content for testing.
func writeStorageTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestStorageListHidesTenantRootForMaster(t *testing.T) {
	baseDir := t.TempDir()
	writeStorageTestFile(t, filepath.Join(baseDir, "master.txt"), "master")
	writeStorageTestFile(t, filepath.Join(baseDir, "tenants", "tenant-a", "secret.txt"), "tenant-secret")

	handler := NewStorageHandler(baseDir)
	req := httptest.NewRequest("GET", "/v1/storage/files", nil)
	w := httptest.NewRecorder()

	handler.handleList(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Files []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for _, f := range resp.Files {
		if f.Path == "tenants" || strings.HasPrefix(f.Path, "tenants/") {
			t.Fatalf("master storage unexpectedly exposed tenant path %q", f.Path)
		}
	}
}

func TestStorageListSubpathTenantReturnsNotFound(t *testing.T) {
	baseDir := t.TempDir()
	writeStorageTestFile(t, filepath.Join(baseDir, "tenants", "tenant-a", "secret.txt"), "tenant-secret")

	handler := NewStorageHandler(baseDir)
	req := httptest.NewRequest("GET", "/v1/storage/files?path=tenants", nil)
	w := httptest.NewRecorder()

	handler.handleList(w, req)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestStorageReadTenantRootReturnsNotFoundForMaster(t *testing.T) {
	baseDir := t.TempDir()
	writeStorageTestFile(t, filepath.Join(baseDir, "tenants", "tenant-a", "secret.txt"), "tenant-secret")

	handler := NewStorageHandler(baseDir)
	req := httptest.NewRequest("GET", "/v1/storage/files/tenants/tenant-a/secret.txt", nil)
	req.SetPathValue("path", "tenants/tenant-a/secret.txt")
	w := httptest.NewRecorder()

	handler.handleRead(w, req)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestStorageSizeExcludesTenantRootForMaster(t *testing.T) {
	baseDir := t.TempDir()
	writeStorageTestFile(t, filepath.Join(baseDir, "master.txt"), "12345")
	writeStorageTestFile(t, filepath.Join(baseDir, "tenants", "tenant-a", "secret.txt"), "1234567890")

	handler := NewStorageHandler(baseDir)
	req := httptest.NewRequest("GET", "/v1/storage/size", nil)
	w := httptest.NewRecorder()

	handler.handleSize(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := w.Body.String()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) == 0 {
		t.Fatal("expected SSE response body")
	}

	// Find the final "done" event.
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "data: ") {
		t.Fatalf("unexpected SSE line %q", last)
	}

	var payload struct {
		Total int64 `json:"total"`
		Files int   `json:"files"`
		Done  bool  `json:"done"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(last, "data: ")), &payload); err != nil {
		t.Fatalf("unmarshal SSE payload: %v", err)
	}
	if !payload.Done {
		t.Fatalf("expected final SSE payload, got %#v", payload)
	}
	if payload.Total != 5 {
		t.Fatalf("total = %d, want 5 (tenant files should be excluded)", payload.Total)
	}
	if payload.Files != 1 {
		t.Fatalf("files = %d, want 1 (tenant files should be excluded)", payload.Files)
	}
}

func TestStorageDeleteInvalidatesSizeCache(t *testing.T) {
	baseDir := t.TempDir()
	writeStorageTestFile(t, filepath.Join(baseDir, "tmp.txt"), "abc")

	handler := NewStorageHandler(baseDir)
	req := httptest.NewRequest(http.MethodDelete, "/v1/storage/files/tmp.txt", nil)
	req.SetPathValue("path", "tmp.txt")

	sizeBase := handler.tenantBaseDir(req)
	handler.sizeCache.Store(sizeBase, &sizeCacheEntry{total: 3, files: 1})

	w := httptest.NewRecorder()
	handler.handleDelete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if _, ok := handler.sizeCache.Load(sizeBase); ok {
		t.Fatal("expected size cache entry to be invalidated after delete")
	}
}

func TestStorageMoveInvalidatesSizeCache(t *testing.T) {
	baseDir := t.TempDir()
	writeStorageTestFile(t, filepath.Join(baseDir, "from.txt"), "abc")

	handler := NewStorageHandler(baseDir)
	req := httptest.NewRequest(http.MethodPut, "/v1/storage/move?from=from.txt&to=to.txt", nil)

	sizeBase := handler.tenantBaseDir(req)
	handler.sizeCache.Store(sizeBase, &sizeCacheEntry{total: 3, files: 1})

	w := httptest.NewRecorder()
	handler.handleMove(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if _, ok := handler.sizeCache.Load(sizeBase); ok {
		t.Fatal("expected size cache entry to be invalidated after move")
	}
}
