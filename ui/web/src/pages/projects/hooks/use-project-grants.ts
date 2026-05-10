import { useState, useCallback, useEffect, useRef } from "react";
import i18next from "i18next";
import { useWs } from "@/hooks/use-ws";
import { useAuthStore } from "@/stores/use-auth-store";
import { Methods } from "@/api/protocol";
import { toast } from "@/stores/use-toast-store";
import { userFriendlyError } from "@/lib/error-utils";
import type { ProjectGrant, ProjectRole } from "@/types/project";

export function useProjectGrants(projectId: string | null | undefined) {
  const ws = useWs();
  const connected = useAuthStore((s) => s.connected);
  const [direct, setDirect] = useState<ProjectGrant[]>([]);
  const [inherited, setInherited] = useState<ProjectGrant[]>([]);
  const [loadingDirect, setLoadingDirect] = useState(false);
  const [loadingInherited, setLoadingInherited] = useState(false);

  // Mirror `direct` into a ref so revokeGrant can snapshot the row being removed
  // synchronously, without a stale-closure capture inside `useCallback` (which
  // would resurrect concurrently-revoked rows on rollback) and without relying
  // on the setState updater callback (Strict Mode invokes it twice in dev,
  // overwriting any closed-over capture variable on the second pass).
  const directRef = useRef<ProjectGrant[]>([]);
  useEffect(() => {
    directRef.current = direct;
  }, [direct]);

  const loadDirect = useCallback(async () => {
    if (!connected || !projectId) return;
    setLoadingDirect(true);
    try {
      const res = await ws.call<{ grants: ProjectGrant[] }>(Methods.PROJECT_GRANTS_LIST, { projectId });
      setDirect(res.grants ?? []);
    } catch (err) {
      toast.error(i18next.t("projects:errors.loadFailed"), userFriendlyError(err));
    } finally {
      setLoadingDirect(false);
    }
  }, [ws, connected, projectId]);

  const loadInherited = useCallback(async () => {
    if (!connected || !projectId) return;
    setLoadingInherited(true);
    try {
      const res = await ws.call<{ grants: ProjectGrant[] }>(Methods.PROJECT_GRANTS_LIST_INHERITED, { projectId });
      setInherited(res.grants ?? []);
    } catch (err) {
      toast.error(i18next.t("projects:errors.loadFailed"), userFriendlyError(err));
    } finally {
      setLoadingInherited(false);
    }
  }, [ws, connected, projectId]);

  const addGrant = useCallback(
    async (userId: string, role: ProjectRole): Promise<boolean> => {
      if (!projectId) return false;
      // Optimistic insert. UUID v4 keeps the placeholder id collision-free even
      // under burst clicks within the same millisecond.
      const optimisticId =
        typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
          ? `optimistic-${crypto.randomUUID()}`
          : `optimistic-${Date.now()}-${Math.random().toString(36).slice(2)}-${userId}`;
      const optimistic: ProjectGrant = {
        id: optimisticId,
        projectId,
        userId,
        teamId: null,
        role,
        grantedBy: null,
        createdAt: new Date().toISOString(),
      };
      setDirect((prev) => [...prev, optimistic]);
      try {
        const res = await ws.call<{ grant: ProjectGrant }>(Methods.PROJECT_GRANTS_CREATE, {
          projectId,
          userId,
          role,
        });
        // Replace optimistic with server-issued grant
        setDirect((prev) => prev.map((g) => (g.id === optimistic.id ? res.grant : g)));
        toast.success(i18next.t("projects:toast.granted"));
        return true;
      } catch (err) {
        // Rollback
        setDirect((prev) => prev.filter((g) => g.id !== optimistic.id));
        toast.error(i18next.t("projects:errors.grantFailed"), userFriendlyError(err));
        return false;
      }
    },
    [ws, projectId],
  );

  const addGrantsBulk = useCallback(
    async (userIds: string[], role: ProjectRole): Promise<number> => {
      let okCount = 0;
      for (const uid of userIds) {
        const ok = await addGrant(uid, role);
        if (ok) okCount += 1;
      }
      return okCount;
    },
    [addGrant],
  );

  const revokeGrant = useCallback(
    async (grantId: string): Promise<boolean> => {
      // Snapshot the row from the ref BEFORE dispatching the optimistic remove —
      // safe under concurrent revokes because each call captures its own row.
      const removed = directRef.current.find((g) => g.id === grantId);
      setDirect((prev) => prev.filter((g) => g.id !== grantId));
      try {
        await ws.call(Methods.PROJECT_GRANTS_DELETE, { id: grantId });
        toast.success(i18next.t("projects:toast.revoked"));
        return true;
      } catch (err) {
        if (removed) {
          // Functional reinsert — leaves concurrent successful revokes alone.
          setDirect((prev) => (prev.some((g) => g.id === grantId) ? prev : [...prev, removed]));
        }
        toast.error(i18next.t("projects:errors.revokeFailed"), userFriendlyError(err));
        return false;
      }
    },
    [ws],
  );

  return {
    direct,
    inherited,
    loadingDirect,
    loadingInherited,
    loadDirect,
    loadInherited,
    addGrant,
    addGrantsBulk,
    revokeGrant,
  };
}
