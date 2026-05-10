import { z } from "zod";

// Scope is mutually exclusive: at most one of teamId/projectId is set.
// global → both NULL; team → teamId required; project → projectId required.
export const McpScopeEnum = z.enum(["global", "team", "project"]);

export const mcpFormSchema = z
  .object({
    name: z.string().min(1),
    displayName: z.string(),
    transport: z.enum(["stdio", "sse", "streamable-http"]),
    command: z.string(),
    args: z.string(),
    url: z.string(),
    headers: z.record(z.string(), z.string()),
    env: z.record(z.string(), z.string()),
    toolPrefix: z.string(),
    timeout: z.number().min(1),
    enabled: z.boolean(),
    requireUserCreds: z.boolean(),
    scope: McpScopeEnum.optional(),
    teamId: z.string().uuid().optional(),
    projectId: z.string().uuid().optional(),
  })
  .superRefine((d, ctx) => {
    const scope = d.scope ?? "global";
    if (scope === "global" && (d.teamId || d.projectId)) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["scope"],
        message: "validation.mcpScopeGlobalNoIds",
      });
    }
    if (scope === "team" && !d.teamId) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["teamId"],
        message: "validation.mcpScopeTeamRequiresId",
      });
    }
    if (scope === "project" && !d.projectId) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["projectId"],
        message: "validation.mcpScopeProjectRequiresId",
      });
    }
    if (scope === "team" && d.projectId) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["projectId"],
        message: "validation.mcpScopeMutex",
      });
    }
    if (scope === "project" && d.teamId) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["teamId"],
        message: "validation.mcpScopeMutex",
      });
    }
  });

export type MCPFormData = z.infer<typeof mcpFormSchema>;
export type McpScope = z.infer<typeof McpScopeEnum>;
