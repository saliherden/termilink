package security

import "gopkg.in/yaml.v3"

type Authorizer struct {
	allowed map[int64]struct{}
	owner   int64
}

func New(owner int64, allowedUsers []int64) *Authorizer {
	allowed := make(map[int64]struct{}, len(allowedUsers))
	for _, id := range allowedUsers {
		allowed[id] = struct{}{}
	}
	return &Authorizer{allowed: allowed, owner: owner}
}

func NewFromConfig(data []byte) (*Authorizer, error) {
	var s struct {
		Owner        int64   `yaml:"owner"`
		AllowedUsers []int64 `yaml:"allowed_users"`
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Owner == 0 && len(s.AllowedUsers) > 0 {
		s.Owner = s.AllowedUsers[0]
	}
	return New(s.Owner, s.AllowedUsers), nil
}

func (a *Authorizer) IsAllowed(userID int64) bool {
	if a == nil {
		return false
	}
	_, ok := a.allowed[userID]
	return ok
}

func (a *Authorizer) IsOwner(userID int64) bool {
	return a != nil && userID == a.owner
}

func (a *Authorizer) AllowedCount() int {
	return len(a.allowed)
}