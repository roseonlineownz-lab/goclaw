import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import type { AdminUser } from "@/hooks/use-admin-users";

const mocks = vi.hoisted(() => ({
  getMock: vi.fn(),
  postMock: vi.fn(),
}));

vi.mock("@/hooks/use-ws", () => ({
  useHttp: () => ({ get: mocks.getMock, post: mocks.postMock }),
  useWs: () => ({ call: vi.fn() }),
}));

import { useAdminUsers } from "@/hooks/use-admin-users";

const { getMock, postMock } = mocks;

const sample: AdminUser = {
  id: "00000000-0000-0000-0000-000000000001",
  email: "user@example.com",
  display_name: "User",
  role: "member",
  status: "active",
  created_at: "2026-05-07T00:00:00Z",
  updated_at: "2026-05-07T00:00:00Z",
};

describe("useAdminUsers", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  it("load() GETs /v1/users and stores results", async () => {
    getMock.mockResolvedValueOnce({ users: [sample] });
    const { result } = renderHook(() => useAdminUsers());
    await act(async () => {
      await result.current.load();
    });
    expect(getMock).toHaveBeenCalledWith("/v1/users");
    expect(result.current.users).toEqual([sample]);
  });

  it("createUser() POSTs full payload + prepends to list", async () => {
    postMock.mockResolvedValueOnce(sample);
    const { result } = renderHook(() => useAdminUsers());
    await act(async () => {
      await result.current.createUser({
        email: sample.email,
        display_name: "User",
        password: "Strong-Pass-1234!",
        role: "member",
      });
    });
    expect(postMock).toHaveBeenCalledWith("/v1/users", {
      email: sample.email,
      display_name: "User",
      password: "Strong-Pass-1234!",
      role: "member",
    });
    expect(result.current.users[0]).toEqual(sample);
  });

  it("load() captures error on rejection", async () => {
    getMock.mockRejectedValueOnce(Object.assign(new Error("forbidden"), { code: "forbidden" }));
    const { result } = renderHook(() => useAdminUsers());
    await act(async () => {
      await result.current.load();
    });
    expect(result.current.error).toBeTruthy();
  });
});
