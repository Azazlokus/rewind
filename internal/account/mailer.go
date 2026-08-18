package account

import (
	"context"
	"log/slog"
)

// Mailer доставляет письма верификации email и сброса пароля (итерация 37). Реального
// SMTP в проекте нет: прод подключает свою реализацию через account.WithMailer, а по
// умолчанию работает LogMailer (для dev — печатает токен в лог, письма не шлёт).
type Mailer interface {
	// SendVerification доставляет токен подтверждения email.
	SendVerification(ctx context.Context, email, token string) error
	// SendPasswordReset доставляет токен сброса пароля.
	SendPasswordReset(ctx context.Context, email, token string) error
}

// LogMailer — дефолтная dev-реализация: логирует токен, чтобы разработчик мог пройти
// флоу без почтового сервиса. НЕ для продакшена (токен верификации/сброса в логе —
// секрет; в проде ставьте настоящий Mailer через account.WithMailer).
type LogMailer struct {
	Log *slog.Logger // nil — slog.Default()
}

func (m LogMailer) SendVerification(_ context.Context, email, token string) error {
	m.logger().Warn("DEV mailer: email verification token (configure a real Mailer for prod)",
		"email", email, "token", token)
	return nil
}

func (m LogMailer) SendPasswordReset(_ context.Context, email, token string) error {
	m.logger().Warn("DEV mailer: password reset token (configure a real Mailer for prod)",
		"email", email, "token", token)
	return nil
}

func (m LogMailer) logger() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}
