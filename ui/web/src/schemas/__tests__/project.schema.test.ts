import { describe, it, expect } from "vitest";
import {
  ProjectSlug,
  projectCreateSchema,
  projectGrantCreateSchema,
  projectUpdateStatusSchema,
} from "@/schemas/project.schema";

const validUUID = "00000000-0000-4000-8000-000000000001";
const validUUID2 = "00000000-0000-4000-8000-000000000002";

describe("ProjectSlug", () => {
  it("accepts kebab-case lowercase slugs", () => {
    expect(ProjectSlug.safeParse("foo-bar").success).toBe(true);
    expect(ProjectSlug.safeParse("a1b2-c3").success).toBe(true);
    expect(ProjectSlug.safeParse("simple").success).toBe(true);
  });

  it("rejects uppercase, underscores, leading/trailing hyphens", () => {
    expect(ProjectSlug.safeParse("FooBar").success).toBe(false);
    expect(ProjectSlug.safeParse("foo_bar").success).toBe(false);
    expect(ProjectSlug.safeParse("-foo").success).toBe(false);
    expect(ProjectSlug.safeParse("foo-").success).toBe(false);
  });

  it("enforces length 3-64", () => {
    expect(ProjectSlug.safeParse("ab").success).toBe(false);
    expect(ProjectSlug.safeParse("a".repeat(65)).success).toBe(false);
    expect(ProjectSlug.safeParse("abc").success).toBe(true);
  });
});

describe("projectCreateSchema", () => {
  it("requires slug", () => {
    const r = projectCreateSchema.safeParse({});
    expect(r.success).toBe(false);
  });

  it("accepts valid slug + optional metadata", () => {
    const r = projectCreateSchema.safeParse({
      slug: "my-proj",
      metadata: { description: "x" },
    });
    expect(r.success).toBe(true);
  });

  it("rejects invalid uuid for ownerUserId", () => {
    const r = projectCreateSchema.safeParse({
      slug: "my-proj",
      ownerUserId: "not-a-uuid",
    });
    expect(r.success).toBe(false);
  });
});

describe("projectGrantCreateSchema", () => {
  it("requires exactly one of userId/teamId", () => {
    expect(
      projectGrantCreateSchema.safeParse({
        projectId: validUUID,
        role: "viewer",
      }).success,
    ).toBe(false);
    expect(
      projectGrantCreateSchema.safeParse({
        projectId: validUUID,
        userId: validUUID2,
        teamId: validUUID2,
        role: "viewer",
      }).success,
    ).toBe(false);
  });

  it("accepts user grant", () => {
    expect(
      projectGrantCreateSchema.safeParse({
        projectId: validUUID,
        userId: validUUID2,
        role: "member",
      }).success,
    ).toBe(true);
  });

  it("accepts team grant", () => {
    expect(
      projectGrantCreateSchema.safeParse({
        projectId: validUUID,
        teamId: validUUID2,
        role: "editor",
      }).success,
    ).toBe(true);
  });

  it("rejects invalid role", () => {
    const r = projectGrantCreateSchema.safeParse({
      projectId: validUUID,
      userId: validUUID2,
      role: "owner",
    });
    expect(r.success).toBe(false);
  });
});

describe("projectUpdateStatusSchema", () => {
  it("accepts active and archived", () => {
    expect(projectUpdateStatusSchema.safeParse({ id: validUUID, status: "active" }).success).toBe(true);
    expect(projectUpdateStatusSchema.safeParse({ id: validUUID, status: "archived" }).success).toBe(true);
  });

  it("rejects unknown status", () => {
    expect(projectUpdateStatusSchema.safeParse({ id: validUUID, status: "deleted" }).success).toBe(false);
  });
});
