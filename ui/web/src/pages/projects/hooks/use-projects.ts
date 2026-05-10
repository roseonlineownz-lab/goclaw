import { useState, useCallback, useRef } from "react";
import i18next from "i18next";
import { useWs } from "@/hooks/use-ws";
import { useAuthStore } from "@/stores/use-auth-store";
import { Methods } from "@/api/protocol";
import { toast } from "@/stores/use-toast-store";
import { userFriendlyError } from "@/lib/error-utils";
import type { Project, ProjectStatus } from "@/types/project";

interface CreateInput {
  slug: string;
  ownerUserId?: string;
  metadata?: Record<string, unknown> | null;
}

interface UpdateMetadataInput {
  id: string;
  slug?: string;
  metadata?: Record<string, unknown> | null;
}

export function useProjects() {
  const ws = useWs();
  const connected = useAuthStore((s) => s.connected);
  const [projects, setProjects] = useState<Project[]>([]);
  // Initialize false — `loading=true` only while a fetch is in flight. Earlier
  // `useState(true)` created a stuck-spinner state when the WS hadn't connected
  // yet (load() early-returns without flipping it). useDeferredLoading on the
  // consumer side already debounces the visible spinner, so cold-start jitter
  // is minimal.
  const [loading, setLoading] = useState(false);
  // Preserve the last filter so post-mutation reloads (create/delete) don't
  // accidentally drop the active status filter the page is showing.
  const lastFilterRef = useRef<{ status?: ProjectStatus | "all"; ownerUserId?: string } | undefined>(undefined);

  const load = useCallback(
    async (filter?: { status?: ProjectStatus | "all"; ownerUserId?: string }) => {
      if (filter !== undefined) lastFilterRef.current = filter;
      if (!connected) return;
      setLoading(true);
      try {
        const params: Record<string, unknown> = {};
        if (filter?.status && filter.status !== "all") params.status = filter.status;
        if (filter?.ownerUserId) params.ownerUserId = filter.ownerUserId;
        const res = await ws.call<{ projects: Project[] }>(Methods.PROJECTS_LIST, params);
        setProjects(res.projects ?? []);
      } catch (err) {
        toast.error(i18next.t("projects:errors.loadFailed"), userFriendlyError(err));
      } finally {
        setLoading(false);
      }
    },
    [ws, connected],
  );

  const get = useCallback(
    async (id: string): Promise<Project | null> => {
      try {
        const res = await ws.call<{ project: Project }>(Methods.PROJECTS_GET, { id });
        return res.project ?? null;
      } catch (err) {
        toast.error(i18next.t("projects:errors.loadFailed"), userFriendlyError(err));
        return null;
      }
    },
    [ws],
  );

  const createProject = useCallback(
    async (input: CreateInput): Promise<Project | null> => {
      try {
        const res = await ws.call<{ project: Project }>(Methods.PROJECTS_CREATE, {
          slug: input.slug,
          ownerUserId: input.ownerUserId,
          metadata: input.metadata ?? null,
        });
        toast.success(i18next.t("projects:toast.created"));
        await load(lastFilterRef.current);
        return res.project ?? null;
      } catch (err) {
        toast.error(i18next.t("projects:errors.createFailed"), userFriendlyError(err));
        throw err;
      }
    },
    [ws, load],
  );

  const updateMetadata = useCallback(
    async (input: UpdateMetadataInput): Promise<Project | null> => {
      try {
        const params: Record<string, unknown> = { id: input.id, metadata: input.metadata ?? null };
        if (input.slug !== undefined) params.slug = input.slug;
        const res = await ws.call<{ ok: boolean; project?: Project }>(
          Methods.PROJECTS_UPDATE_METADATA,
          params,
        );
        toast.success(i18next.t("projects:toast.updated"));
        return res.project ?? null;
      } catch (err) {
        toast.error(i18next.t("projects:errors.updateFailed"), userFriendlyError(err));
        throw err;
      }
    },
    [ws],
  );

  const updateStatus = useCallback(
    async (id: string, status: ProjectStatus) => {
      try {
        await ws.call(Methods.PROJECTS_UPDATE_STATUS, { id, status });
        toast.success(i18next.t("projects:toast.updated"));
        await load(lastFilterRef.current);
      } catch (err) {
        toast.error(i18next.t("projects:errors.updateFailed"), userFriendlyError(err));
        throw err;
      }
    },
    [ws, load],
  );

  const deleteProject = useCallback(
    async (id: string) => {
      try {
        await ws.call(Methods.PROJECTS_DELETE, { id });
        toast.success(i18next.t("projects:toast.deleted"));
        await load(lastFilterRef.current);
      } catch (err) {
        toast.error(i18next.t("projects:errors.deleteFailed"), userFriendlyError(err));
        throw err;
      }
    },
    [ws, load],
  );

  return {
    projects,
    loading,
    load,
    get,
    createProject,
    updateMetadata,
    updateStatus,
    deleteProject,
  };
}
