export type PermissionScope = "global" | "team" | "project" | "agent" | "user";

export interface FolderPermission {
  folder: string;
  write: boolean;
  edit: boolean;
  delete: boolean;
}
