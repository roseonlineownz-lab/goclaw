package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
)

// resetPasswordCmd: operator-level password recovery for v4.
//
// There is no email-based reset flow in v4.0. When a root password is lost
// or a user is locked out, an operator with DB access invokes:
//
//	goclaw reset-password --email user@example.com
//
// The new password is read from stdin without echo. On success, the user's
// password_hash is updated AND every active refresh-token session for that
// user is revoked atomically.
func resetPasswordCmd() *cobra.Command {
	var email string

	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset a user's password via DB (operator-level recovery)",
		Long: `Reset a user's password directly via the database.

Reads the new password from stdin (terminal input is not echoed).
Validates the password against the same complexity policy as bootstrap/login,
hashes it with Argon2id, and atomically revokes all of the user's active
refresh-token sessions.

This bypasses email-based reset (v4.0 has no email infrastructure) and is
intended for operator-level recovery only.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if email == "" {
				return errors.New("--email is required")
			}
			email = strings.ToLower(strings.TrimSpace(email))

			pw, err := readPasswordFromStdin()
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			if err := auth.ValidatePasswordComplexity(pw); err != nil {
				return fmt.Errorf("password rejected: %w", err)
			}

			hash, err := auth.HashPassword(pw)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}

			dsn, err := resolveDSN()
			if err != nil {
				return err
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer db.Close()

			ctx := context.Background()
			return resetPasswordTx(ctx, db, email, hash)
		},
	}

	cmd.Flags().StringVar(&email, "email", "", "user email (required)")
	_ = cmd.MarkFlagRequired("email")
	return cmd
}

// readPasswordFromStdin reads a password from the terminal without echoing,
// then prompts for confirmation. Mismatch → error.
func readPasswordFromStdin() (string, error) {
	fmt.Print("New password: ")
	first, err := term.ReadPassword(0)
	fmt.Println()
	if err != nil {
		return "", err
	}
	fmt.Print("Confirm: ")
	second, err := term.ReadPassword(0)
	fmt.Println()
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	if len(first) == 0 {
		return "", errors.New("empty password")
	}
	return string(first), nil
}

// resetPasswordTx runs the UPDATE + bulk revoke inside a single TX.
// On commit: stale sessions cannot accept the old password's hash anymore.
func resetPasswordTx(ctx context.Context, db *sql.DB, email, hash string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash=$1, updated_at=NOW() WHERE email=$2 AND deleted_at IS NULL`,
		hash, email,
	)
	if err != nil {
		return fmt.Errorf("update users: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("user not found: %s", email)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at=NOW()
		   WHERE user_id = (SELECT id FROM users WHERE email=$1)
		     AND revoked_at IS NULL`,
		email,
	); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("password reset for %s — all active sessions revoked\n", email)
	return nil
}
