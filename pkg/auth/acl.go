package auth

import (
	"sync"
)

type Authorizer struct {
	mu          sync.RWMutex
	maintainers map[string][]maintainerEntry
	privacy     map[string]privacySetting
}

type maintainerEntry struct {
	User  string
	Group string
}

type privacySetting struct {
	IsPrivate     bool
	AllowedUsers  []string
	AllowedGroups []string
}

func NewAuthorizer() *Authorizer {
	return &Authorizer{
		maintainers: make(map[string][]maintainerEntry),
		privacy:     make(map[string]privacySetting),
	}
}

func (a *Authorizer) SetProjectMaintainer(project, user, group string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.maintainers[project] = append(a.maintainers[project], maintainerEntry{
		User:  user,
		Group: group,
	})
}

func (a *Authorizer) SetProjectPrivacy(project string, isPrivate bool, users, groups []string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.privacy[project] = privacySetting{
		IsPrivate:     isPrivate,
		AllowedUsers:  users,
		AllowedGroups: groups,
	}
}

func (a *Authorizer) CanWrite(user *User, project string) bool {
	if user.IsSuperuser {
		return true
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	entries, exists := a.maintainers[project]
	if !exists {
		return false
	}

	for _, entry := range entries {
		if entry.User != "" && entry.User == user.Username {
			return true
		}
		if entry.Group != "" && contains(user.Groups, entry.Group) {
			return true
		}
	}

	return false
}

func (a *Authorizer) CanRead(user *User, project string) bool {
	if user.IsSuperuser {
		return true
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	setting, exists := a.privacy[project]
	if !exists || !setting.IsPrivate {
		return true // Public by default
	}

	if contains(setting.AllowedUsers, user.Username) {
		return true
	}

	for _, g := range user.Groups {
		if contains(setting.AllowedGroups, g) {
			return true
		}
	}

	return false
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}
