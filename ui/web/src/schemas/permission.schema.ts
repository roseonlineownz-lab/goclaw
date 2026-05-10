import { z } from "zod";

export const PermissionScopeEnum = z.enum([
  "global",
  "team",
  "project",
  "agent",
  "user",
]);

export const folderPermissionSchema = z.object({
  folder: z.string().min(1, "validation.required"),
  write: z.boolean(),
  edit: z.boolean(),
  delete: z.boolean(),
});

export type FolderPermissionInput = z.infer<typeof folderPermissionSchema>;
