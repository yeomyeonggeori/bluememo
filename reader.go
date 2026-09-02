package bluememo

import "strings"

type Reader struct {
	PersonID          string
	MemberCircleIDs   []string
	ReadableCircleIDs []string
	SecurityLevelRank int
	GrantedClasses    []string
}

func NewReader(personID string, memberCircleIDs []string, containedCircles map[string][]string, securityLevelRank int, grantedClasses []string) Reader {
	member := NormalizeCircleIDs(memberCircleIDs)
	return Reader{
		PersonID:          strings.TrimSpace(personID),
		MemberCircleIDs:   member,
		ReadableCircleIDs: ReadableCircles(member, containedCircles),
		SecurityLevelRank: securityLevelRank,
		GrantedClasses:    nonNilStrings(grantedClasses),
	}
}

func ReadableCircles(memberCircleIDs []string, containedCircles map[string][]string) []string {
	readable := map[string]bool{}
	pending := NormalizeCircleIDs(memberCircleIDs)
	for len(pending) > 0 {
		circleID := pending[0]
		pending = pending[1:]
		if readable[circleID] {
			continue
		}
		readable[circleID] = true
		pending = append(pending, NormalizeCircleIDs(containedCircles[circleID])...)
	}
	circleIDs := make([]string, 0, len(readable))
	for circleID := range readable {
		circleIDs = append(circleIDs, circleID)
	}
	return NormalizeCircleIDs(circleIDs)
}

func (reader Reader) CanRead(fact Fact) bool {
	if !reader.isInScope(fact) {
		return false
	}
	if fact.SecurityLevelRank > reader.SecurityLevelRank {
		return false
	}
	for _, requiredClass := range fact.RequiredClasses {
		if !containsString(reader.GrantedClasses, requiredClass) {
			return false
		}
	}
	return true
}

func (reader Reader) CanWriteCircle(circleID string) bool {
	return containsString(reader.MemberCircleIDs, strings.ToLower(strings.TrimSpace(circleID)))
}

func (reader Reader) isInScope(fact Fact) bool {
	switch fact.ScopeType {
	case ScopeTypePrivate:
		return reader.PersonID != "" && fact.OwnerPersonID == reader.PersonID
	case ScopeTypeCircle:
		for _, circleID := range fact.CircleIDs {
			if containsString(reader.ReadableCircleIDs, circleID) {
				return true
			}
		}
		return false
	case ScopeTypeWorkspace:
		return true
	}
	return false
}
