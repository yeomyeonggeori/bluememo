package main

import "strings"

type person struct {
	personID string
	names    []string
}

// registryResolver fills an identifier only when a surface form in the text is
// answered by exactly one person. Several answers resolve nothing; the surface
// form is reported as unresolved so the gap is visible rather than silent.
type registryResolver struct {
	people []person
}

func (resolver registryResolver) Resolve(content string) ([]string, []string) {
	answersBySurface := map[string][]string{}
	for _, candidate := range resolver.people {
		for _, name := range candidate.names {
			if strings.Contains(content, name) {
				answersBySurface[name] = appendUnique(answersBySurface[name], candidate.personID)
			}
		}
	}

	resolved := []string{}
	unresolved := []string{}
	for surface, personIDs := range answersBySurface {
		if len(personIDs) == 1 {
			resolved = appendUnique(resolved, personIDs[0])
			continue
		}
		unresolved = appendUnique(unresolved, surface)
	}
	return resolved, unresolved
}

func appendUnique(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}
