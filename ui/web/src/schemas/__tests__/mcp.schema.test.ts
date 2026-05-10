import { describe, it, expect } from "vitest";
import { McpScopeEnum, mcpFormSchema } from "@/schemas/mcp.schema";

const validUUID = "00000000-0000-4000-8000-000000000001";

describe("McpScopeEnum", () => {
  it("accepts global, team, project", () => {
    expect(McpScopeEnum.safeParse("global").success).toBe(true);
    expect(McpScopeEnum.safeParse("team").success).toBe(true);
    expect(McpScopeEnum.safeParse("project").success).toBe(true);
  });

  it("rejects tenant", () => {
    expect(McpScopeEnum.safeParse("tenant").success).toBe(false);
  });
});

const baseForm = {
  name: "srv",
  displayName: "Srv",
  transport: "stdio" as const,
  command: "echo",
  args: "",
  url: "",
  headers: {},
  env: {},
  toolPrefix: "",
  timeout: 30,
  enabled: true,
  requireUserCreds: false,
};

describe("mcpFormSchema scope mutual exclusion", () => {
  it("global: both ids must be empty", () => {
    expect(
      mcpFormSchema.safeParse({ ...baseForm, scope: "global" }).success,
    ).toBe(true);
    expect(
      mcpFormSchema.safeParse({
        ...baseForm,
        scope: "global",
        teamId: validUUID,
      }).success,
    ).toBe(false);
  });

  it("team: teamId required, projectId forbidden", () => {
    expect(
      mcpFormSchema.safeParse({ ...baseForm, scope: "team" }).success,
    ).toBe(false);
    expect(
      mcpFormSchema.safeParse({ ...baseForm, scope: "team", teamId: validUUID })
        .success,
    ).toBe(true);
    expect(
      mcpFormSchema.safeParse({
        ...baseForm,
        scope: "team",
        teamId: validUUID,
        projectId: validUUID,
      }).success,
    ).toBe(false);
  });

  it("project: projectId required, teamId forbidden", () => {
    expect(
      mcpFormSchema.safeParse({ ...baseForm, scope: "project" }).success,
    ).toBe(false);
    expect(
      mcpFormSchema.safeParse({
        ...baseForm,
        scope: "project",
        projectId: validUUID,
      }).success,
    ).toBe(true);
    expect(
      mcpFormSchema.safeParse({
        ...baseForm,
        scope: "project",
        projectId: validUUID,
        teamId: validUUID,
      }).success,
    ).toBe(false);
  });

  it("scope omitted defaults to global behaviour", () => {
    expect(mcpFormSchema.safeParse(baseForm).success).toBe(true);
  });
});
