import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, UserCog } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { PageHeader } from "@/components/shared/page-header";
import { EmptyState } from "@/components/shared/empty-state";
import { TableSkeleton } from "@/components/shared/loading-skeleton";
import { useDeferredLoading } from "@/hooks/use-deferred-loading";
import { formatDate } from "@/lib/format";
import { useAdminUsers } from "@/hooks/use-admin-users";
import { AdminCreateUserDialog } from "./admin-create-user-dialog";

export function AdminUsersPage() {
  const { t } = useTranslation("auth");
  const { users, loading, load, createUser } = useAdminUsers();
  const showSkeleton = useDeferredLoading(loading && users.length === 0);
  const [createOpen, setCreateOpen] = useState(false);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="p-4 sm:p-6 pb-10">
      <PageHeader
        title={t("adminUsers.title")}
        description={t("adminUsers.description")}
        actions={
          <Button onClick={() => setCreateOpen(true)} className="gap-1">
            <Plus className="h-4 w-4" />
            {t("adminUsers.createButton")}
          </Button>
        }
      />

      <div className="mt-6">
        {showSkeleton ? (
          <TableSkeleton rows={6} />
        ) : users.length === 0 ? (
          <EmptyState
            icon={UserCog}
            title={t("adminUsers.emptyTitle")}
            description={t("adminUsers.emptyDescription")}
          />
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <table className="min-w-[600px] w-full text-sm">
              <thead className="bg-muted/50 text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-3 py-2 text-left font-medium">{t("adminUsers.columns.email")}</th>
                  <th className="px-3 py-2 text-left font-medium">{t("adminUsers.columns.displayName")}</th>
                  <th className="px-3 py-2 text-left font-medium">{t("adminUsers.columns.role")}</th>
                  <th className="px-3 py-2 text-left font-medium">{t("adminUsers.columns.status")}</th>
                  <th className="px-3 py-2 text-left font-medium">{t("adminUsers.columns.created")}</th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id} className="border-t">
                    <td className="px-3 py-2 font-mono text-xs">{u.email}</td>
                    <td className="px-3 py-2">{u.display_name ?? "—"}</td>
                    <td className="px-3 py-2">
                      <Badge variant="outline" className="text-[11px]">
                        {t(`adminUsers.roles.${u.role}`, { defaultValue: u.role })}
                      </Badge>
                    </td>
                    <td className="px-3 py-2">
                      <Badge variant={u.status === "active" ? "default" : "secondary"} className="text-[11px]">
                        {t(`adminUsers.status.${u.status}`, { defaultValue: u.status })}
                      </Badge>
                    </td>
                    <td className="px-3 py-2 text-xs text-muted-foreground">{formatDate(u.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <AdminCreateUserDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onSubmit={async (data) => {
          await createUser(data);
        }}
      />
    </div>
  );
}
