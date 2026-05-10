import { describe, it, expect } from "vitest";
import {
  passwordResetRequestSchema,
  passwordResetConfirmSchema,
} from "@/schemas/password-reset.schema";
import { adminCreateUserSchema } from "@/schemas/admin-create-user.schema";

describe("passwordResetRequestSchema", () => {
  it("accepts a valid email", () => {
    expect(passwordResetRequestSchema.safeParse({ email: "user@example.com" }).success).toBe(true);
  });
  it("rejects an invalid email", () => {
    const r = passwordResetRequestSchema.safeParse({ email: "not-an-email" });
    expect(r.success).toBe(false);
  });
});

describe("passwordResetConfirmSchema", () => {
  const goodPwd = "Strong-Pass-1234!";

  it("accepts a strong matching password", () => {
    const r = passwordResetConfirmSchema.safeParse({
      token: "abc",
      password: goodPwd,
      confirm: goodPwd,
    });
    expect(r.success).toBe(true);
  });

  it("rejects mismatched confirm", () => {
    const r = passwordResetConfirmSchema.safeParse({
      token: "abc",
      password: goodPwd,
      confirm: goodPwd + "x",
    });
    expect(r.success).toBe(false);
  });

  it("rejects weak password", () => {
    const r = passwordResetConfirmSchema.safeParse({
      token: "abc",
      password: "short",
      confirm: "short",
    });
    expect(r.success).toBe(false);
  });

  it("rejects empty token", () => {
    const r = passwordResetConfirmSchema.safeParse({ token: "", password: goodPwd, confirm: goodPwd });
    expect(r.success).toBe(false);
  });
});

describe("adminCreateUserSchema", () => {
  const baseGood = {
    email: "u@example.com",
    display_name: "User",
    password: "Strong-Pass-1234!",
    role: "member" as const,
  };

  it("accepts a valid member payload", () => {
    expect(adminCreateUserSchema.safeParse(baseGood).success).toBe(true);
  });

  it("accepts viewer role", () => {
    expect(adminCreateUserSchema.safeParse({ ...baseGood, role: "viewer" }).success).toBe(true);
  });

  it("accepts admin role at the schema level (BE 403s non-root callers)", () => {
    // Schema is permissive so the BE remains source of truth. The dialog
    // gates the `admin` option on caller role; non-owners never see it.
    const r = adminCreateUserSchema.safeParse({ ...baseGood, role: "admin" });
    expect(r.success).toBe(true);
  });

  it("rejects unknown role values", () => {
    const r = adminCreateUserSchema.safeParse({ ...baseGood, role: "root" });
    expect(r.success).toBe(false);
  });

  it("rejects invalid email", () => {
    const r = adminCreateUserSchema.safeParse({ ...baseGood, email: "bad" });
    expect(r.success).toBe(false);
  });

  it("rejects empty display_name", () => {
    const r = adminCreateUserSchema.safeParse({ ...baseGood, display_name: "" });
    expect(r.success).toBe(false);
  });
});
