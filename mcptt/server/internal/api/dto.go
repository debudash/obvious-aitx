package api

import (
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

// Wire DTOs — the only shapes the API serializes. store.User carries the
// password hash and must never be marshaled directly.

type userDTO struct {
	ID              string    `json:"id"`
	Username        string    `json:"username"`
	DisplayName     string    `json:"displayName"`
	Role            string    `json:"role"`
	Priority        int       `json:"priority"`
	FunctionalAlias string    `json:"functionalAlias,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

type groupDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

type affiliationDTO struct {
	UserID    string    `json:"userId"`
	GroupID   string    `json:"groupId"`
	State     string    `json:"state"`
	ChangedAt time.Time `json:"changedAt"`
}

func toUserDTO(u store.User) userDTO {
	return userDTO{
		ID:              u.ID,
		Username:        u.Username,
		DisplayName:     u.DisplayName,
		Role:            string(u.Role),
		Priority:        u.Priority,
		FunctionalAlias: u.FunctionalAlias,
		CreatedAt:       u.CreatedAt,
	}
}

func toGroupDTO(g store.Group) groupDTO {
	return groupDTO{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		CreatedBy:   g.CreatedBy,
		CreatedAt:   g.CreatedAt,
	}
}

func toAffiliationDTO(a store.Affiliation) affiliationDTO {
	return affiliationDTO{
		UserID:    a.UserID,
		GroupID:   a.GroupID,
		State:     a.State,
		ChangedAt: a.ChangedAt,
	}
}
