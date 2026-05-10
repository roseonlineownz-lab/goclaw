import { z } from "zod";

// Mirror backend auth.ValidatePasswordComplexity (≥12 chars + letter + digit + symbol).
const passwordPolicy = z
  .string()
  .min(12, "min12")
  .refine((v) => /[A-Za-z]/.test(v), "needsLetter")
  .refine((v) => /[0-9]/.test(v), "needsDigit")
  .refine((v) => /[^A-Za-z0-9]/.test(v), "needsSymbol");

export const passwordResetRequestSchema = z.object({
  email: z.string().email("invalidEmail"),
});

export const passwordResetConfirmSchema = z
  .object({
    token: z.string().min(1, "required"),
    password: passwordPolicy,
    confirm: z.string().min(1, "required"),
  })
  .refine((d) => d.password === d.confirm, {
    message: "passwordsDontMatch",
    path: ["confirm"],
  });

export type PasswordResetRequestData = z.infer<typeof passwordResetRequestSchema>;
export type PasswordResetConfirmData = z.infer<typeof passwordResetConfirmSchema>;
