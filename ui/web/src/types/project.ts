export type ProjectStatus = "active" | "archived";
export type ProjectRole = "viewer" | "member" | "editor";

export interface Project {
  id: string;
  slug: string;
  ownerUserId: string;
  status: ProjectStatus;
  metadata?: Record<string, unknown> | null;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectGrant {
  id: string;
  projectId: string;
  userId?: string | null;
  teamId?: string | null;
  role: ProjectRole;
  grantedBy?: string | null;
  createdAt: string;
}

export interface ProjectInput {
  slug: string;
  ownerUserId?: string;
  metadata?: Record<string, unknown> | null;
}

export interface ProjectGrantInput {
  projectId: string;
  userId?: string;
  teamId?: string;
  role: ProjectRole;
}
