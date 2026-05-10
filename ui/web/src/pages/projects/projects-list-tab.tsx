import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { FolderKanban, LayoutGrid, List, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { TooltipProvider, Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip";
import { CardSkeleton } from "@/components/shared/loading-skeleton";
import { ConfirmDialog } from "@/components/shared/confirm-dialog";
import { EmptyState } from "@/components/shared/empty-state";
import { Pagination } from "@/components/shared/pagination";
import { SearchInput } from "@/components/shared/search-input";
import { useDeferredLoading } from "@/hooks/use-deferred-loading";
import { usePagination } from "@/hooks/use-pagination";
import { useProjects } from "./hooks/use-projects";
import { ProjectCard } from "./project-card";
import { ProjectListRow } from "./project-list-row";
import { ProjectCreateDialog } from "./project-create-dialog";
import type { ProjectStatus } from "@/types/project";

interface ProjectsListTabProps {
  onSelectProject: (id: string) => void;
}

type StatusFilter = ProjectStatus | "all";

export function ProjectsListTab({ onSelectProject }: ProjectsListTabProps) {
  const { t } = useTranslation("projects");
  const { t: tc } = useTranslation("common");
  const { projects, loading, load, createProject, deleteProject } = useProjects();
  const showSkeleton = useDeferredLoading(loading && projects.length === 0);

  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("active");
  const [viewMode, setViewMode] = useState<"card" | "list">("list");
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<{ id: string; name: string } | null>(null);

  useEffect(() => {
    load({ status: statusFilter });
  }, [load, statusFilter]);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return projects;
    return projects.filter((p) => {
      const meta = (p.metadata ?? {}) as Record<string, unknown>;
      const name = typeof meta.displayName === "string" ? meta.displayName.toLowerCase() : "";
      const desc = typeof meta.description === "string" ? meta.description.toLowerCase() : "";
      return p.slug.toLowerCase().includes(q) || name.includes(q) || desc.includes(q);
    });
  }, [projects, search]);

  const { pageItems, pagination, setPage, setPageSize } = usePagination(filtered);

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <SearchInput value={search} onChange={setSearch} placeholder={t("searchPlaceholder")} className="max-w-sm" />
        <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v as StatusFilter)}>
          <SelectTrigger className="h-9 w-[140px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="active">{t("status.active")}</SelectItem>
            <SelectItem value="archived">{t("status.archived")}</SelectItem>
            <SelectItem value="all">{t("status.all")}</SelectItem>
          </SelectContent>
        </Select>
        <Button onClick={() => setCreateOpen(true)} className="ml-auto gap-1 sm:ml-0">
          <Plus className="h-4 w-4" /> {t("createButton")}
        </Button>
        <TooltipProvider>
          <div className="ml-auto flex items-center gap-0.5 rounded-md border p-0.5">
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant={viewMode === "list" ? "default" : "ghost"}
                  size="xs"
                  className="h-7 w-7 p-0"
                  aria-label={t("viewList")}
                  onClick={() => setViewMode("list")}
                >
                  <List className="h-3.5 w-3.5" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>{t("viewList")}</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant={viewMode === "card" ? "default" : "ghost"}
                  size="xs"
                  className="h-7 w-7 p-0"
                  aria-label={t("viewCard")}
                  onClick={() => setViewMode("card")}
                >
                  <LayoutGrid className="h-3.5 w-3.5" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>{t("viewCard")}</TooltipContent>
            </Tooltip>
          </div>
        </TooltipProvider>
      </div>

      <div className="mt-6">
        {showSkeleton ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {Array.from({ length: 6 }).map((_, i) => (
              <CardSkeleton key={i} />
            ))}
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={FolderKanban}
            title={search ? t("noMatchTitle") : t("emptyTitle")}
            description={search ? tc("tryDifferentSearch") : t("emptyDescription")}
          />
        ) : (
          <>
            {viewMode === "card" ? (
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {pageItems.map((p) => {
                  const meta = (p.metadata ?? {}) as Record<string, unknown>;
                  const display = typeof meta.displayName === "string" && meta.displayName ? meta.displayName : p.slug;
                  return (
                    <ProjectCard
                      key={p.id}
                      project={p}
                      onClick={() => onSelectProject(p.id)}
                      onDelete={() => setDeleteTarget({ id: p.id, name: display })}
                    />
                  );
                })}
              </div>
            ) : (
              <div className="flex flex-col gap-2">
                {pageItems.map((p) => {
                  const meta = (p.metadata ?? {}) as Record<string, unknown>;
                  const display = typeof meta.displayName === "string" && meta.displayName ? meta.displayName : p.slug;
                  return (
                    <ProjectListRow
                      key={p.id}
                      project={p}
                      onClick={() => onSelectProject(p.id)}
                      onDelete={() => setDeleteTarget({ id: p.id, name: display })}
                    />
                  );
                })}
              </div>
            )}
            <div className="mt-4">
              <Pagination
                page={pagination.page}
                pageSize={pagination.pageSize}
                total={pagination.total}
                totalPages={pagination.totalPages}
                onPageChange={setPage}
                onPageSizeChange={setPageSize}
              />
            </div>
          </>
        )}
      </div>

      <ProjectCreateDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onSubmit={async (data) => {
          await createProject(data);
        }}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={() => setDeleteTarget(null)}
        title={t("delete.title")}
        description={t("delete.description", { name: deleteTarget?.name ?? "" })}
        confirmLabel={t("delete.confirmLabel")}
        variant="destructive"
        onConfirm={async () => {
          if (deleteTarget) {
            await deleteProject(deleteTarget.id);
            setDeleteTarget(null);
          }
        }}
      />
    </>
  );
}
