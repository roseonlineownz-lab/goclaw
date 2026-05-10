import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { UserPlus, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ConfirmDialog } from "@/components/shared/confirm-dialog";
import { EmptyState } from "@/components/shared/empty-state";
import { useContactResolver } from "@/hooks/use-contact-resolver";
import { formatUserLabel } from "@/lib/format-user-label";
import { ProjectGrantRow } from "../components/project-grant-row";
import { ProjectBulkGrantDialog } from "../components/project-bulk-grant-dialog";
import { useProjectGrants } from "../hooks/use-project-grants";
import type { ProjectGrant } from "@/types/project";

interface ProjectMembersTabProps {
  projectId: string;
}

export function ProjectMembersTab({ projectId }: ProjectMembersTabProps) {
  const { t } = useTranslation("projects");
  const grants = useProjectGrants(projectId);
  const [subTab, setSubTab] = useState<"direct" | "inherited">("direct");
  const [addOpen, setAddOpen] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<ProjectGrant | null>(null);

  useEffect(() => {
    grants.loadDirect();
  }, [grants.loadDirect]);

  // Lazy-load inherited only when its sub-tab opens.
  useEffect(() => {
    if (subTab === "inherited") grants.loadInherited();
  }, [subTab, grants.loadInherited]);

  const directIds = useMemo(
    () => grants.direct.map((g) => g.userId).filter((u): u is string => Boolean(u)),
    [grants.direct],
  );
  const { resolve: revokeResolve } = useContactResolver(revokeTarget?.userId ? [revokeTarget.userId] : []);

  return (
    <Tabs value={subTab} onValueChange={(v) => setSubTab(v as "direct" | "inherited")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <TabsList>
          <TabsTrigger value="direct">{t("members.subTabs.direct")}</TabsTrigger>
          <TabsTrigger value="inherited">{t("members.subTabs.inherited")}</TabsTrigger>
        </TabsList>
        {subTab === "direct" && (
          <Button onClick={() => setAddOpen(true)} className="gap-1" size="sm">
            <UserPlus className="h-4 w-4" />
            {t("members.addButton")}
          </Button>
        )}
      </div>

      <TabsContent value="direct" className="mt-4 space-y-2">
        {grants.direct.length === 0 ? (
          <EmptyState
            icon={Users}
            title={t("members.directEmptyTitle")}
            description={t("members.directEmptyDescription")}
          />
        ) : (
          grants.direct.map((g) => (
            <ProjectGrantRow key={g.id} grant={g} onRevoke={(target) => setRevokeTarget(target)} />
          ))
        )}
      </TabsContent>

      <TabsContent value="inherited" className="mt-4 space-y-2">
        {grants.inherited.length === 0 ? (
          <EmptyState
            icon={Users}
            title={t("members.inheritedEmptyTitle")}
            description={t("members.inheritedEmptyDescription")}
          />
        ) : (
          grants.inherited.map((g) => (
            <ProjectGrantRow
              key={g.id}
              grant={g}
              teamLabel={g.teamId ?? undefined}
            />
          ))
        )}
      </TabsContent>

      <ProjectBulkGrantDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        excludeUserIds={directIds}
        onSubmit={async (userIds, role) => {
          await grants.addGrantsBulk(userIds, role);
        }}
      />

      <ConfirmDialog
        open={!!revokeTarget}
        onOpenChange={() => setRevokeTarget(null)}
        title={t("members.removeConfirmTitle")}
        description={t("members.removeConfirmDescription", {
          user: revokeTarget?.userId ? formatUserLabel(revokeTarget.userId, revokeResolve) : "",
        })}
        confirmLabel={t("members.removeConfirmLabel")}
        variant="destructive"
        onConfirm={async () => {
          if (revokeTarget) {
            await grants.revokeGrant(revokeTarget.id);
            setRevokeTarget(null);
          }
        }}
      />
    </Tabs>
  );
}
