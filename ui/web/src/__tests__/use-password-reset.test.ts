import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  postMock: vi.fn(),
  getMock: vi.fn(),
}));

vi.mock("@/hooks/use-ws", () => ({
  useHttp: () => ({ post: mocks.postMock, get: mocks.getMock }),
  useWs: () => ({ call: vi.fn() }),
}));

import { usePasswordReset } from "@/hooks/use-password-reset";

const { postMock } = mocks;

describe("usePasswordReset", () => {
  beforeEach(() => {
    postMock.mockReset();
  });

  it("request() POSTs to /v1/auth/password-reset/request with email", async () => {
    postMock.mockResolvedValueOnce(undefined);
    const { result } = renderHook(() => usePasswordReset());
    await act(async () => {
      await result.current.request("user@example.com");
    });
    expect(postMock).toHaveBeenCalledWith("/v1/auth/password-reset/request", {
      email: "user@example.com",
    });
    expect(result.current.requesting).toBe(false);
  });

  it("request() rethrows on failure (so rate-limit can be surfaced)", async () => {
    postMock.mockRejectedValueOnce(Object.assign(new Error("rl"), { code: "rate_limit_exceeded" }));
    const { result } = renderHook(() => usePasswordReset());
    await expect(
      act(async () => {
        await result.current.request("u@e.com");
      }),
    ).rejects.toThrow("rl");
  });

  it("confirm() POSTs token + new_password (snake_case payload)", async () => {
    postMock.mockResolvedValueOnce(undefined);
    const { result } = renderHook(() => usePasswordReset());
    await act(async () => {
      await result.current.confirm("token-abc", "Strong-Pass-1234!");
    });
    expect(postMock).toHaveBeenCalledWith("/v1/auth/password-reset/confirm", {
      token: "token-abc",
      new_password: "Strong-Pass-1234!",
    });
  });

  it("confirm() rejects on invalid token", async () => {
    postMock.mockRejectedValueOnce(Object.assign(new Error("invalid"), { code: "invalid_credentials" }));
    const { result } = renderHook(() => usePasswordReset());
    await expect(
      act(async () => {
        await result.current.confirm("expired", "Strong-Pass-1234!");
      }),
    ).rejects.toThrow("invalid");
  });
});
