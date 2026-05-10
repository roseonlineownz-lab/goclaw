import { z } from "zod";

// Mirror backend auth.ValidatePasswordComplexity.
const passwordPolicy = z
  .string()
  .min(12, "min12")
  .refine((v) => /[A-Za-z]/.test(v), "needsLetter")
  .refine((v) => /[0-9]/.test(v), "needsDigit")
  .refine((v) => /[^A-Za-z0-9]/.test(v), "needsSymbol");

// Schema mirrors what the BE accepts: admin/member/viewer. Root creation is
// bootstrap-only and never exposed via API. The dialog gates the `admin`
// option on the caller's role (only owners see it) — the schema stays
// permissive so the BE remains the source of truth via 403 on non-root.
export const adminCreateUserRoleEnum = z.enum(["admin", "member", "viewer"]);

export const adminCreateUserSchema = z.object({
  email: z.string().email("invalidEmail"),
  display_name: z.string().min(1, "required").max(64, "displayNameTooLong"),
  password: passwordPolicy,
  role: adminCreateUserRoleEnum,
});

export type AdminCreateUserData = z.infer<typeof adminCreateUserSchema>;
export type AdminCreateUserRole = z.infer<typeof adminCreateUserRoleEnum>;
