import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDate } from "@/lib/format";
import type { Project } from "@/types/project";

interface ProjectOverviewTabProps {
  project: Project;
}

export function ProjectOverviewTab({ project }: ProjectOverviewTabProps) {
  const { t } = useTranslation("projects");
  const meta = (project.metadata ?? {}) as Record<string, unknown>;
  const displayName = typeof meta.displayName === "string" ? meta.displayName : project.slug;
  const description = typeof meta.description === "string" ? meta.description : null;

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>{displayName}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          {description ? (
            <p className="whitespace-pre-wrap text-muted-foreground">{description}</p>
          ) : (
            <p className="italic text-muted-foreground">{t("overview.noDescription")}</p>
          )}
          <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <dt className="text-xs uppercase tracking-wide text-muted-foreground">{t("columns.slug")}</dt>
              <dd className="mt-0.5 font-mono text-sm">{project.slug}</dd>
            </div>
            <div>
              <dt className="text-xs uppercase tracking-wide text-muted-foreground">{t("detail.owner")}</dt>
              <dd className="mt-0.5 font-mono text-xs">{project.ownerUserId}</dd>
            </div>
            <div>
              <dt className="text-xs uppercase tracking-wide text-muted-foreground">{t("columns.status")}</dt>
              <dd className="mt-0.5">
                <Badge variant={project.status === "active" ? "default" : "secondary"}>
                  {t(`status.${project.status}`)}
                </Badge>
              </dd>
            </div>
            <div>
              <dt className="text-xs uppercase tracking-wide text-muted-foreground">{t("detail.createdAt")}</dt>
              <dd className="mt-0.5">{formatDate(project.createdAt)}</dd>
            </div>
            <div>
              <dt className="text-xs uppercase tracking-wide text-muted-foreground">{t("detail.updatedAt")}</dt>
              <dd className="mt-0.5">{formatDate(project.updatedAt)}</dd>
            </div>
          </dl>
        </CardContent>
      </Card>
    </div>
  );
}
