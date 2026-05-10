import { describe, it, expect } from "vitest";
import { HookScopeEnum } from "@/schemas/hooks.schema";

describe("HookScopeEnum", () => {
  it("accepts global, user, agent (v4 valid scopes)", () => {
    expect(HookScopeEnum.safeParse("global").success).toBe(true);
    expect(HookScopeEnum.safeParse("user").success).toBe(true);
    expect(HookScopeEnum.safeParse("agent").success).toBe(true);
  });

  it("rejects tenant (v3 scope removed in v4)", () => {
    expect(HookScopeEnum.safeParse("tenant").success).toBe(false);
  });

  it("rejects unknown values", () => {
    expect(HookScopeEnum.safeParse("project").success).toBe(false);
    expect(HookScopeEnum.safeParse("").success).toBe(false);
  });
});
