import { z } from "zod";

// Slug: kebab-case lowercase letters, numbers, hyphens. 3-64 chars.
export const ProjectSlug = z
  .string()
  .min(3)
  .max(64)
  .regex(/^[a-z0-9]+(-[a-z0-9]+)*$/, "validation.invalidSlug");

export const ProjectStatusEnum = z.enum(["active", "archived"]);
export const ProjectRoleEnum = z.enum(["viewer", "member", "editor"]);

export const projectCreateSchema = z.object({
  slug: ProjectSlug,
  ownerUserId: z.string().uuid().optional(),
  metadata: z.record(z.string(), z.unknown()).nullable().optional(),
});

export const projectUpdateMetadataSchema = z.object({
  id: z.string().uuid(),
  metadata: z.record(z.string(), z.unknown()).nullable(),
});

export const projectUpdateStatusSchema = z.object({
  id: z.string().uuid(),
  status: ProjectStatusEnum,
});

export const projectGrantCreateSchema = z
  .object({
    projectId: z.string().uuid(),
    userId: z.string().uuid().optional(),
    teamId: z.string().uuid().optional(),
    role: ProjectRoleEnum,
  })
  .refine((d) => Boolean(d.userId) !== Boolean(d.teamId), {
    message: "validation.grantTargetXor",
    path: ["userId"],
  });

export type ProjectCreateInput = z.infer<typeof projectCreateSchema>;
export type ProjectGrantCreateInput = z.infer<typeof projectGrantCreateSchema>;
