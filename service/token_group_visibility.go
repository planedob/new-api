package service

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// GetUserSelectableTokenGroups intersects the established usable-group
// permission with optional visibility policies. With the flag off it returns
// the exact legacy result.
func GetUserSelectableTokenGroups(userId int) (map[string]string, error) {
	user, err := model.GetUserById(userId, false)
	if err != nil {
		return nil, err
	}
	groups := GetUserUsableGroups(user.Group)
	if !model.TokenGroupVisibilityEnabled() {
		return groups, nil
	}
	policies, err := model.GetTokenGroupVisibilityPolicies()
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	for _, policy := range policies {
		if _, usable := groups[policy.Group]; !usable {
			continue
		}
		inWindow := (policy.StartTime == 0 || now >= policy.StartTime) &&
			(policy.EndTime == 0 || now < policy.EndTime)
		switch policy.Visibility {
		case model.TokenGroupVisibilityHidden:
			if inWindow {
				delete(groups, policy.Group)
			}
		case model.TokenGroupVisibilityTargeted:
			if !inWindow {
				delete(groups, policy.Group)
				continue
			}
			allowed := false
			for _, username := range policy.Usernames {
				if username == user.Username {
					allowed = true
					break
				}
			}
			if !allowed {
				delete(groups, policy.Group)
			}
		}
	}
	return groups, nil
}

func ValidateUserSelectableTokenGroup(userId int, group string) error {
	if !model.TokenGroupVisibilityEnabled() || group == "" {
		return nil
	}
	if group == "auto" {
		user, err := model.GetUserById(userId, false)
		if err != nil {
			return err
		}
		autoGroups, err := GetUserSelectableAutoGroups(userId, user.Group)
		if err != nil {
			return err
		}
		if len(autoGroups) == 0 {
			return errors.New("所选令牌分组当前不可用")
		}
		return nil
	}
	groups, err := GetUserSelectableTokenGroups(userId)
	if err != nil {
		return err
	}
	if _, ok := groups[group]; !ok {
		return errors.New("所选令牌分组当前不可用")
	}
	return nil
}

// GetUserSelectableAutoGroups returns only configured auto groups that remain
// selectable for this user. Auto must not bypass hidden or targeted policies.
// A zero userId is kept for legacy/internal callers with no user identity.
func GetUserSelectableAutoGroups(userId int, userGroup string) ([]string, error) {
	autoGroups := GetUserAutoGroup(userGroup)
	if userId <= 0 {
		return autoGroups, nil
	}
	selectableGroups, err := GetUserSelectableTokenGroups(userId)
	if err != nil {
		return nil, err
	}
	filtered := make([]string, 0, len(autoGroups))
	for _, group := range autoGroups {
		if _, ok := selectableGroups[group]; ok {
			filtered = append(filtered, group)
		}
	}
	return filtered, nil
}
