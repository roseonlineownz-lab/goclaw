import { describe, it, expect } from "vitest";
import {
  PermissionScopeEnum,
  folderPermissionSchema,
} from "@/schemas/permission.schema";

describe("PermissionScopeEnum", () => {
  it("accepts canonical scopes", () => {
    for (const s of ["global", "team", "project", "agent", "user"]) {
      expect(PermissionScopeEnum.safeParse(s).success).toBe(true);
    }
  });

  it("rejects tenant (v3 residue)", () => {
    expect(PermissionScopeEnum.safeParse("tenant").success).toBe(false);
  });
});

describe("folderPermissionSchema", () => {
  it("treats write/edit/delete as independent booleans", () => {
    const r = folderPermissionSchema.safeParse({
      folder: "/data",
      write: true,
      edit: false,
      delete: false,
    });
    expect(r.success).toBe(true);
  });

  it("requires non-empty folder", () => {
    const r = folderPermissionSchema.safeParse({
      folder: "",
      write: true,
      edit: false,
      delete: false,
    });
    expect(r.success).toBe(false);
  });

  it("requires all 3 boolean fields", () => {
    const r = folderPermissionSchema.safeParse({
      folder: "/data",
      write: true,
    });
    expect(r.success).toBe(false);
  });
});
