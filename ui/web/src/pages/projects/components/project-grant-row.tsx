import { Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useContactResolver } from "@/hooks/use-contact-resolver";
import { formatUserLabel } from "@/lib/format-user-label";
import { ProjectRoleChip } from "./project-role-chip";
import type { ProjectGrant } from "@/types/project";

interface ProjectGrantRowProps {
  grant: ProjectGrant;
  /** When undefined, the row is read-only (no delete button). */
  onRevoke?: (grant: ProjectGrant) => void;
  /** Display "via team {name}" badge for inherited grants. */
  teamLabel?: string;
}

export function ProjectGrantRow({ grant, onRevoke, teamLabel }: ProjectGrantRowProps) {
  const { t } = useTranslation("projects");
  const ids = grant.userId ? [grant.userId] : [];
  const { resolve } = useContactResolver(ids);
  const userLabel = grant.userId ? formatUserLabel(grant.userId, resolve) : "—";
  const optimistic = grant.id.startsWith("optimistic-");

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded border bg-card px-3 py-2">
      <div className="flex flex-1 flex-wrap items-center gap-2 min-w-0">
        <span className="truncate text-sm font-medium">{userLabel}</span>
        {teamLabel && (
          <Badge variant="secondary" className="text-[11px]">
            {t("members.viaTeam")} {teamLabel}
          </Badge>
        )}
        {optimistic && (
          <Badge variant="outline" className="text-[11px] opacity-60">
            …
          </Badge>
        )}
      </div>
      <ProjectRoleChip value={grant.role} size="sm" />
      {onRevoke && !optimistic && (
        <Button
          variant="ghost"
          size="xs"
          className="h-7 w-7 p-0 text-muted-foreground hover:text-destructive"
          aria-label={t("members.removeConfirmLabel")}
          onClick={() => onRevoke(grant)}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      )}
    </div>
  );
}
