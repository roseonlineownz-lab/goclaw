import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import type { Project } from "@/types/project";

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

import { useProjects } from "../hooks/use-projects";

const sample: Project = {
  id: "11111111-1111-1111-1111-111111111111",
  slug: "acme",
  ownerUserId: "owner",
  status: "active",
  metadata: { displayName: "Acme" },
  createdAt: "2026-05-07T00:00:00Z",
  updatedAt: "2026-05-07T00:00:00Z",
};

describe("useProjects", () => {
  beforeEach(() => {
    callMock.mockReset();
    successMock.mockReset();
    errorMock.mockReset();
  });

  it("load() calls projects.list and stores results", async () => {
    callMock.mockResolvedValueOnce({ projects: [sample] });
    const { result } = renderHook(() => useProjects());
    await act(async () => {
      await result.current.load({ status: "active" });
    });
    expect(callMock).toHaveBeenCalledWith("projects.list", { status: "active" });
    expect(result.current.projects).toEqual([sample]);
  });

  it("load() omits status when 'all'", async () => {
    callMock.mockResolvedValueOnce({ projects: [] });
    const { result } = renderHook(() => useProjects());
    await act(async () => {
      await result.current.load({ status: "all" });
    });
    expect(callMock).toHaveBeenCalledWith("projects.list", {});
  });

  it("createProject calls projects.create with metadata and triggers reload", async () => {
    // first call = create, second call = subsequent load() inside hook
    callMock.mockResolvedValueOnce({ project: sample });
    callMock.mockResolvedValueOnce({ projects: [sample] });
    const { result } = renderHook(() => useProjects());
    let returned: Project | null = null;
    await act(async () => {
      returned = await result.current.createProject({ slug: "acme", metadata: { displayName: "Acme" } });
    });
    expect(callMock).toHaveBeenNthCalledWith(1, "projects.create", {
      slug: "acme",
      ownerUserId: undefined,
      metadata: { displayName: "Acme" },
    });
    expect(returned).toEqual(sample);
    expect(successMock).toHaveBeenCalled();
  });

  it("createProject surfaces errors via toast and rethrows", async () => {
    callMock.mockRejectedValueOnce(new Error("conflict"));
    const { result } = renderHook(() => useProjects());
    await expect(
      act(async () => {
        await result.current.createProject({ slug: "acme" });
      }),
    ).rejects.toThrow("conflict");
    expect(errorMock).toHaveBeenCalled();
  });

  it("deleteProject calls projects.delete and reloads", async () => {
    callMock.mockResolvedValueOnce({ ok: true, archived: true });
    callMock.mockResolvedValueOnce({ projects: [] });
    const { result } = renderHook(() => useProjects());
    await act(async () => {
      await result.current.deleteProject(sample.id);
    });
    expect(callMock).toHaveBeenNthCalledWith(1, "projects.delete", { id: sample.id });
    await waitFor(() => expect(successMock).toHaveBeenCalled());
  });

  it("updateMetadata sends slug only when provided", async () => {
    callMock.mockResolvedValueOnce({ ok: true, project: sample });
    const { result } = renderHook(() => useProjects());
    await act(async () => {
      await result.current.updateMetadata({ id: sample.id, metadata: { foo: "bar" } });
    });
    expect(callMock).toHaveBeenCalledWith("projects.update_metadata", {
      id: sample.id,
      metadata: { foo: "bar" },
    });
  });
});
