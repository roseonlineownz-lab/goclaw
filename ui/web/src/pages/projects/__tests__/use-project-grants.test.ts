import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import type { ProjectGrant } from "@/types/project";

const mocks = vi.hoisted(() => ({
  callMock: vi.fn(),
  successMock: vi.fn(),
  errorMock: vi.fn(),
}));

vi.mock("@/hooks/use-ws", () => ({
  useWs: () => ({ call: mocks.callMock }),
  useHttp: () => ({ get: vi.fn() }),
}));

vi.mock("@/stores/use-auth-store", () => ({
  useAuthStore: <T,>(selector: (s: { connected: boolean }) => T) => selector({ connected: true }),
}));

vi.mock("@/stores/use-toast-store", () => ({
  toast: { success: mocks.successMock, error: mocks.errorMock },
}));

vi.mock("@/lib/error-utils", () => ({
  userFriendlyError: (e: unknown) => String(e),
}));

vi.mock("i18next", () => ({
  default: { t: (k: string) => k },
}));

const { callMock, successMock, errorMock } = mocks;

import { useProjectGrants } from "../hooks/use-project-grants";

const PROJECT = "00000000-0000-0000-0000-000000000aaa";
const sampleGrant: ProjectGrant = {
  id: "grant-1",
  projectId: PROJECT,
  userId: "user-1",
  teamId: null,
  role: "member",
  grantedBy: "owner",
  createdAt: "2026-05-07T00:00:00Z",
};

describe("useProjectGrants", () => {
  beforeEach(() => {
    callMock.mockReset();
    successMock.mockReset();
    errorMock.mockReset();
  });

  it("loadDirect fetches direct grants via project_grants.list", async () => {
    callMock.mockResolvedValueOnce({ grants: [sampleGrant] });
    const { result } = renderHook(() => useProjectGrants(PROJECT));
    await act(async () => {
      await result.current.loadDirect();
    });
    expect(callMock).toHaveBeenCalledWith("project_grants.list", { projectId: PROJECT });
    expect(result.current.direct).toEqual([sampleGrant]);
  });

  it("loadInherited fetches inherited grants separately", async () => {
    callMock.mockResolvedValueOnce({ grants: [] });
    const { result } = renderHook(() => useProjectGrants(PROJECT));
    await act(async () => {
      await result.current.loadInherited();
    });
    expect(callMock).toHaveBeenCalledWith("project_grants.list_inherited", { projectId: PROJECT });
    expect(result.current.inherited).toEqual([]);
  });

  it("addGrant inserts optimistic row then replaces with server grant", async () => {
    callMock.mockResolvedValueOnce({ grant: sampleGrant });
    const { result } = renderHook(() => useProjectGrants(PROJECT));
    await act(async () => {
      const ok = await result.current.addGrant("user-1", "member");
      expect(ok).toBe(true);
    });
    expect(callMock).toHaveBeenCalledWith("project_grants.create", {
      projectId: PROJECT,
      userId: "user-1",
      role: "member",
    });
    expect(result.current.direct).toEqual([sampleGrant]);
    expect(successMock).toHaveBeenCalled();
  });

  it("addGrant rolls back optimistic row on error", async () => {
    callMock.mockRejectedValueOnce(new Error("forbidden"));
    const { result } = renderHook(() => useProjectGrants(PROJECT));
    await act(async () => {
      const ok = await result.current.addGrant("user-1", "viewer");
      expect(ok).toBe(false);
    });
    expect(result.current.direct).toEqual([]);
    expect(errorMock).toHaveBeenCalled();
  });

  it("revokeGrant removes optimistically and rolls back on error", async () => {
    callMock.mockResolvedValueOnce({ grants: [sampleGrant] });
    const { result } = renderHook(() => useProjectGrants(PROJECT));
    await act(async () => {
      await result.current.loadDirect();
    });
    callMock.mockRejectedValueOnce(new Error("forbidden"));
    await act(async () => {
      const ok = await result.current.revokeGrant("grant-1");
      expect(ok).toBe(false);
    });
    await waitFor(() => expect(result.current.direct).toEqual([sampleGrant]));
  });

  it("revokeGrant rollback does not resurrect a concurrently-revoked grant", async () => {
    // Seed two grants then race two revokes: A succeeds, B fails. Pre-fix, the
    // failing revoke restored its closure snapshot which still contained A,
    // resurrecting the already-deleted row. Post-fix uses functional rollback
    // that only re-inserts the specific row B removed.
    const grantA: ProjectGrant = { ...sampleGrant, id: "grant-A", userId: "user-A" };
    const grantB: ProjectGrant = { ...sampleGrant, id: "grant-B", userId: "user-B" };
    callMock.mockResolvedValueOnce({ grants: [grantA, grantB] });
    const { result } = renderHook(() => useProjectGrants(PROJECT));
    await act(async () => {
      await result.current.loadDirect();
    });
    // A succeeds, B fails.
    callMock.mockResolvedValueOnce({ ok: true });
    callMock.mockRejectedValueOnce(new Error("forbidden"));
    await act(async () => {
      const [okA, okB] = await Promise.all([
        result.current.revokeGrant("grant-A"),
        result.current.revokeGrant("grant-B"),
      ]);
      expect(okA).toBe(true);
      expect(okB).toBe(false);
    });
    // Final state: A gone (succeeded), B restored (failed). A must NOT be
    // resurrected by B's rollback.
    const ids = result.current.direct.map((g) => g.id).sort();
    expect(ids).toEqual(["grant-B"]);
  });

  it("addGrant is a no-op when projectId is null", async () => {
    const { result } = renderHook(() => useProjectGrants(null));
    await act(async () => {
      const ok = await result.current.addGrant("user-1", "viewer");
      expect(ok).toBe(false);
    });
    expect(callMock).not.toHaveBeenCalled();
  });
});
