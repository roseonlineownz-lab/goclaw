import { Trash2, FolderKanban } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelativeTime } from "@/lib/format";
import type { Project } from "@/types/project";

interface ProjectListRowProps {
  project: Project;
  onClick: () => void;
  onDelete: () => void;
}

export function ProjectListRow({ project, onClick, onDelete }: ProjectListRowProps) {
  const { t } = useTranslation("projects");
  const meta = (project.metadata ?? {}) as Record<string, unknown>;
  const displayName = typeof meta.displayName === "string" && meta.displayName ? meta.displayName : project.slug;
  const description = typeof meta.description === "string" ? meta.description : null;

  return (
    <button
      type="button"
      onClick={onClick}
      className="group flex w-full items-center gap-3 rounded-md border bg-card px-3 py-2.5 text-left transition-colors hover:bg-accent/50"
    >
      <FolderKanban className="h-5 w-5 shrink-0 text-muted-foreground" aria-hidden />
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <span className="truncate font-medium">{displayName}</span>
        <Badge variant="outline" className="shrink-0 font-mono text-[11px]">
          {project.slug}
        </Badge>
        {project.status === "archived" && (
          <Badge variant="secondary" className="shrink-0 text-[11px]">
            {t("status.archived")}
          </Badge>
        )}
        {description && (
          <span className="hidden truncate text-xs text-muted-foreground sm:inline">— {description}</span>
        )}
      </div>
      <span className="hidden whitespace-nowrap text-xs text-muted-foreground sm:inline">
        {formatRelativeTime(project.createdAt)}
      </span>
      <Button
        variant="ghost"
        size="xs"
        className="h-7 w-7 p-0 text-muted-foreground opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
        aria-label={t("delete.confirmLabel")}
        onClick={(e) => {
          e.stopPropagation();
          onDelete();
        }}
      >
        <Trash2 className="h-3.5 w-3.5" />
      </Button>
    </button>
  );
}
