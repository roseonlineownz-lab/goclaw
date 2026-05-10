// Package email provides outbound notification dispatch. The auth flows
// (password reset, invite) call this to deliver one-time URLs without
// embedding SMTP/templating into the auth domain. v4 rc1 ships only the
// stderr-logging stub; a real SMTP dispatcher lands post-rc1.
package email

import (
	"context"
	"log/slog"
)

// Dispatcher is the minimal surface auth handlers need. Real SMTP
// implementations land post-rc1; rc1 ships StderrDispatcher only.
type Dispatcher interface {
	SendPasswordReset(ctx context.Context, toEmail, resetURL string) error
	SendInvite(ctx context.Context, toEmail, inviteURL string) error
}

// StderrDispatcher logs outbound emails as structured slog records. Used in
// rc1 when no SMTP is configured. slog handlers are concurrent-safe so this
// dispatcher is safe to share across goroutines.
type StderrDispatcher struct {
	Logger *slog.Logger
}

// NewStderrDispatcher returns a stub Dispatcher that logs each call. Pass a
// pre-configured logger; nil falls back to slog.Default.
func NewStderrDispatcher(logger *slog.Logger) *StderrDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &StderrDispatcher{Logger: logger}
}

func (d *StderrDispatcher) SendPasswordReset(_ context.Context, to, url string) error {
	d.Logger.Info("email.stub.password_reset", "to", to, "reset_url", url)
	return nil
}

func (d *StderrDispatcher) SendInvite(_ context.Context, to, url string) error {
	d.Logger.Info("email.stub.invite", "to", to, "invite_url", url)
	return nil
}
