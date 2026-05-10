import { Trash2, FolderKanban } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelativeTime } from "@/lib/format";
import type { Project } from "@/types/project";

interface ProjectCardProps {
  project: Project;
  onClick: () => void;
  onDelete: () => void;
}

export function ProjectCard({ project, onClick, onDelete }: ProjectCardProps) {
  const { t } = useTranslation("projects");
  const meta = (project.metadata ?? {}) as Record<string, unknown>;
  const displayName = typeof meta.displayName === "string" && meta.displayName ? meta.displayName : project.slug;
  const description = typeof meta.description === "string" ? meta.description : null;

  return (
    <Card
      className="group cursor-pointer transition-colors hover:bg-accent/40"
      onClick={onClick}
      onKeyDown={(e) => {
        if (e.key === "Enter") onClick();
      }}
      tabIndex={0}
      role="button"
    >
      <CardHeader className="flex flex-row items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <FolderKanban className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <div className="min-w-0">
            <CardTitle className="truncate text-base">{displayName}</CardTitle>
            <div className="mt-1 flex flex-wrap items-center gap-1">
              <Badge variant="outline" className="font-mono text-[10px]">
                {project.slug}
              </Badge>
              {project.status === "archived" && (
                <Badge variant="secondary" className="text-[10px]">
                  {t("status.archived")}
                </Badge>
              )}
            </div>
          </div>
        </div>
        <Button
          variant="ghost"
          size="xs"
          className="h-7 w-7 shrink-0 p-0 text-muted-foreground opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
          aria-label={t("delete.confirmLabel")}
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </CardHeader>
      <CardContent>
        {description ? (
          <p className="line-clamp-2 text-sm text-muted-foreground">{description}</p>
        ) : (
          <p className="text-sm italic text-muted-foreground">{t("overview.noDescription")}</p>
        )}
        <p className="mt-3 text-xs text-muted-foreground">{formatRelativeTime(project.createdAt)}</p>
      </CardContent>
    </Card>
  );
}
