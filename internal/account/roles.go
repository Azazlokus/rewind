package account

// Роли аккаунтов (итер. 39). Ранг задаёт иерархию прав: user < moderator < admin.
const (
	RoleUser      = "user"
	RoleModerator = "moderator"
	RoleAdmin     = "admin"
)

// RoleRank — числовой ранг роли для сравнения прав (неизвестная роль = ранг user).
func RoleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 2
	case RoleModerator:
		return 1
	default:
		return 0
	}
}

// ValidRole сообщает, известна ли роль (для валидации назначения роли).
func ValidRole(role string) bool {
	return role == RoleUser || role == RoleModerator || role == RoleAdmin
}
