package workspace

import (
	"context"
	"log/slog"
)

// OnProjectCreate ensures the project workspace folder exists after a successful
// DB insert. Call this from the project-create RPC handler (or HTTP handler)
// AFTER the store.Create call commits — DB row is source of truth.
//
// If the folder creation fails, the error is logged but the caller should
// treat it as non-fatal: the DB row exists and the folder can be created on
// the next access (EnsureProjectFolder is idempotent).
//
// baseDir must match the workspace root used by the channel/web resolver so
// project paths and channel/web paths share a single root.
//
// Usage (future projects RPC handler):
//
//	if err := store.Projects.Create(ctx, project); err != nil { ... }
//	if fsErr := workspace.OnProjectCreate(ctx, baseDir, project.Slug); fsErr != nil {
//	    slog.Warn("workspace.project_folder_deferred", "slug", project.Slug)
//	    // continue — do not return error to caller
//	}
func OnProjectCreate(ctx context.Context, baseDir, slug string) error {
	_, err := EnsureProjectFolder(ctx, baseDir, slug)
	if err != nil {
		slog.WarnContext(ctx, "workspace.project_folder_create_failed",
			"slug", slug,
			"err", err,
		)
	}
	return err
}
