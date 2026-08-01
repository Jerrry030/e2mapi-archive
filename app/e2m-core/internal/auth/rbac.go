package auth

import (
	"e2m.local/contracts"
)

// ScopeUser resolves the effective user_id filter for neutral user-scoped
// resources such as audit logs and secrets. Domain resources should use
// ScopeOwnerUser or ScopeSupplierUser so account ownership alone never implies a
// business permission.
func ScopeUser(user contracts.User, requested int64) (int64, error) {
	if IsPlatformAdmin(user) {
		return requested, nil
	}
	if user.ID == 0 || len(user.Roles) == 0 {
		return 0, ErrForbidden
	}
	if requested == 0 || requested == user.ID {
		return user.ID, nil
	}
	return 0, ErrForbidden
}

func ScopeOwnerUser(user contracts.User, requested int64) (int64, error) {
	return scopeUserWithRole(user, requested, contracts.UserRoleOwner)
}

func ScopeSupplierUser(user contracts.User, requested int64) (int64, error) {
	return scopeUserWithRole(user, requested, contracts.UserRoleSupplier)
}

func scopeUserWithRole(user contracts.User, requested int64, role contracts.UserRole) (int64, error) {
	if IsPlatformAdmin(user) {
		return requested, nil
	}
	if user.ID == 0 || !HasRole(user, role) {
		return 0, ErrForbidden
	}
	if requested == 0 || requested == user.ID {
		return user.ID, nil
	}
	return 0, ErrForbidden
}

// CanReadUser reports whether user may read neutral user-scoped data.
func CanReadUser(user contracts.User, userID int64) bool {
	if IsPlatformAdmin(user) {
		return true
	}
	return user.ID != 0 && user.ID == userID && len(user.Roles) > 0
}

func CanReadOwnerUser(user contracts.User, userID int64) bool {
	if IsPlatformAdmin(user) {
		return true
	}
	return userID != 0 && user.ID == userID && HasRole(user, contracts.UserRoleOwner)
}

func CanReadSupplierUser(user contracts.User, userID int64) bool {
	if IsPlatformAdmin(user) {
		return true
	}
	return userID != 0 && user.ID == userID && HasRole(user, contracts.UserRoleSupplier)
}

func CanWriteOwnerUser(user contracts.User, userID int64) bool {
	return CanReadOwnerUser(user, userID)
}

func CanWriteSupplierUser(user contracts.User, userID int64) bool {
	return CanReadSupplierUser(user, userID)
}

// IsPlatformAdmin reports whether user manages the platform itself.
func IsPlatformAdmin(user contracts.User) bool {
	return HasRole(user, contracts.UserRolePlatformAdmin)
}

func IsOwner(user contracts.User) bool {
	return HasRole(user, contracts.UserRoleOwner)
}

func IsSupplier(user contracts.User) bool {
	return HasRole(user, contracts.UserRoleSupplier)
}

func HasRole(user contracts.User, role contracts.UserRole) bool {
	normalizedRole, ok := NormalizeRole(role)
	if !ok {
		normalizedRole = role
	}
	for _, r := range user.Roles {
		normalizedCurrent, ok := NormalizeRole(r)
		if !ok {
			normalizedCurrent = r
		}
		if normalizedCurrent == normalizedRole {
			return true
		}
	}
	return false
}
