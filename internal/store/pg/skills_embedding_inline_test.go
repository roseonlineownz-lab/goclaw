package pg

import (
	"fmt"
	"strings"
	"testing"
)

// buildSkillEmbeddingQueryString replicates the SQL construction logic from
// SearchByEmbedding so we can lock the output shape before removing the helper.
func buildSkillEmbeddingQueryString(scopeCond string, orderN, limitN int) string {
	cond := ""
	if scopeCond != "" {
		expr := strings.TrimPrefix(scopeCond, " AND ")
		cond = fmt.Sprintf(" AND (source = 'builtin' OR (%s))", expr)
	}
	return fmt.Sprintf(`SELECT name, slug, COALESCE(description, '') AS description, version, file_path,
			1 - (embedding <=> $1::halfvec) AS score
		FROM skills
		WHERE status = 'active' AND enabled = true AND embedding IS NOT NULL
		  AND visibility != 'private'%s
		ORDER BY embedding <=> $%d::halfvec
		LIMIT $%d`, cond, orderN, limitN)
}

// TestSkillEmbeddingSQL_NoScope locks the SQL output when no project scope is
// active — the common path in single-tenant v4.
func TestSkillEmbeddingSQL_NoScope(t *testing.T) {
	got := buildSkillEmbeddingQueryString("", 2, 3)
	want := `SELECT name, slug, COALESCE(description, '') AS description, version, file_path,
			1 - (embedding <=> $1::halfvec) AS score
		FROM skills
		WHERE status = 'active' AND enabled = true AND embedding IS NOT NULL
		  AND visibility != 'private'
		ORDER BY embedding <=> $2::halfvec
		LIMIT $3`
	if got != want {
		t.Fatalf("SQL mismatch (no scope):\nwant: %q\n got: %q", want, got)
	}
}

// TestSkillEmbeddingSQL_WithProjectScope locks the SQL output when a project
// scope condition is present. Builtins remain visible regardless of project.
func TestSkillEmbeddingSQL_WithProjectScope(t *testing.T) {
	scope := " AND project_id = $2"
	got := buildSkillEmbeddingQueryString(scope, 3, 4)
	want := `SELECT name, slug, COALESCE(description, '') AS description, version, file_path,
			1 - (embedding <=> $1::halfvec) AS score
		FROM skills
		WHERE status = 'active' AND enabled = true AND embedding IS NOT NULL
		  AND visibility != 'private' AND (source = 'builtin' OR (project_id = $2))
		ORDER BY embedding <=> $3::halfvec
		LIMIT $4`
	if got != want {
		t.Fatalf("SQL mismatch (project scope):\nwant: %q\n got: %q", want, got)
	}
}
