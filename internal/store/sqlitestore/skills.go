//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const defaultSkillsCacheTTL = 5 * time.Minute

// skillFrontmatterRe matches YAML frontmatter (--- delimited) at the start of a file.
var skillFrontmatterRe = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n?`)

// SQLiteSkillStore implements store.SkillStore backed by SQLite.
type SQLiteSkillStore struct {
	db      *sql.DB
	baseDir string
	mu      sync.RWMutex
	version atomic.Int64

	listCache map[uuid.UUID]*skillListCacheEntry
	ttl       time.Duration
}

type skillListCacheEntry struct {
	skills []store.SkillInfo
	ver    int64
	time   time.Time
}

func NewSQLiteSkillStore(db *sql.DB, baseDir string) *SQLiteSkillStore {
	return &SQLiteSkillStore{
		db:        db,
		baseDir:   baseDir,
		listCache: make(map[uuid.UUID]*skillListCacheEntry),
		ttl:       defaultSkillsCacheTTL,
	}
}

func (s *SQLiteSkillStore) Version() int64 { return s.version.Load() }
func (s *SQLiteSkillStore) BumpVersion()   { s.version.Store(time.Now().UnixMilli()) }
func (s *SQLiteSkillStore) Dirs() []string { return []string{s.baseDir} }

func (s *SQLiteSkillStore) ListSkills(ctx context.Context) []store.SkillInfo {
	currentVer := s.version.Load()

	s.mu.RLock()
	if entry := s.listCache[uuid.Nil]; entry != nil && entry.ver == currentVer && time.Since(entry.time) < s.ttl {
		result := entry.skills
		s.mu.RUnlock()
		return result
	}
	s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, description, visibility, tags, version, source, status, enabled, deps, frontmatter, file_path
		 FROM skills WHERE (status IN ('active', 'archived') OR source = 'builtin')
		 ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []store.SkillInfo
	for rows.Next() {
		var id uuid.UUID
		var name, slug, visibility, status, source string
		var desc *string
		var tagsJSON []byte
		var version int
		var enabled bool
		var depsRaw, fmRaw []byte
		var filePath *string
		if err := rows.Scan(&id, &name, &slug, &desc, &visibility, &tagsJSON, &version,
			&source, &status, &enabled, &depsRaw, &fmRaw, &filePath); err != nil {
			continue
		}
		info := buildSkillInfo(id.String(), name, slug, desc, version, s.baseDir, filePath)
		info.Visibility = visibility
		scanJSONStringArray(tagsJSON, &info.Tags)
		info.Source = source
		info.Status = status
		info.Enabled = enabled
		info.MissingDeps = parseDepsColumn(depsRaw)
		info.Author = parseFrontmatterAuthor(fmRaw)
		result = append(result, info)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ListSkills: rows iteration error", "error", err)
		return nil
	}

	s.mu.Lock()
	s.listCache[uuid.Nil] = &skillListCacheEntry{skills: result, ver: currentVer, time: time.Now()}
	s.mu.Unlock()

	return result
}

func (s *SQLiteSkillStore) ListAllSkills(ctx context.Context) []store.SkillInfo {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, description, visibility, tags, version, source, status, enabled, deps, file_path
		 FROM skills WHERE enabled = 1 AND status != 'deleted'
		 ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return s.scanSkillInfoList(rows)
}

func (s *SQLiteSkillStore) ListAllSystemSkills(ctx context.Context) []store.SkillInfo {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, description, visibility, tags, version, source, status, enabled, deps, file_path
		 FROM skills WHERE source = 'builtin' AND enabled = 1 AND status != 'deleted'
		 ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return s.scanSkillInfoList(rows)
}

func (s *SQLiteSkillStore) scanSkillInfoList(rows *sql.Rows) []store.SkillInfo {
	var result []store.SkillInfo
	for rows.Next() {
		var id uuid.UUID
		var name, slug, visibility, status, source string
		var desc *string
		var tagsJSON []byte
		var version int
		var enabled bool
		var depsRaw []byte
		var filePath *string
		if err := rows.Scan(&id, &name, &slug, &desc, &visibility, &tagsJSON, &version,
			&source, &status, &enabled, &depsRaw, &filePath); err != nil {
			continue
		}
		info := buildSkillInfo(id.String(), name, slug, desc, version, s.baseDir, filePath)
		info.Visibility = visibility
		scanJSONStringArray(tagsJSON, &info.Tags)
		info.Source = source
		info.Status = status
		info.Enabled = enabled
		info.MissingDeps = parseDepsColumn(depsRaw)
		result = append(result, info)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("scanSkillInfoList: rows iteration error", "error", err)
	}
	return result
}

func (s *SQLiteSkillStore) StoreMissingDeps(ctx context.Context, id uuid.UUID, missing []string) error {
	encoded, err := marshalMissingDeps(missing)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE skills SET deps = ?, updated_at = ? WHERE id = ?`,
		encoded, time.Now().UTC(), id,
	)
	if err == nil {
		s.BumpVersion()
	}
	return err
}

func marshalMissingDeps(missing []string) ([]byte, error) {
	if missing == nil {
		missing = []string{}
	}
	return json.Marshal(map[string]any{"missing": missing})
}

// --- helpers shared with other skill files ---

func buildSkillInfo(id, name, slug string, desc *string, version int, baseDir string, filePath *string) store.SkillInfo {
	d := ""
	if desc != nil {
		d = *desc
	}
	skillDir := fmt.Sprintf("%s/%s/%d", baseDir, slug, version)
	if filePath != nil && *filePath != "" {
		skillDir = *filePath
	}
	return store.SkillInfo{
		ID:          id,
		Name:        name,
		Slug:        slug,
		Path:        skillDir + "/SKILL.md",
		BaseDir:     skillDir,
		Source:      "managed",
		Description: d,
		Version:     version,
	}
}

func readSkillFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = skillFrontmatterRe.ReplaceAllString(content, "")
	return content, nil
}

func parseDepsColumn(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var d struct {
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if len(d.Missing) == 0 {
		return nil
	}
	return d.Missing
}

func parseFrontmatterAuthor(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var fm map[string]string
	if err := json.Unmarshal(raw, &fm); err != nil {
		return ""
	}
	return fm["author"]
}

func marshalFrontmatter(fm map[string]string) []byte {
	if len(fm) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(fm)
	if err != nil {
		return []byte("{}")
	}
	return b
}
