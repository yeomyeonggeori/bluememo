package main

import "runtime/debug"

func serverVersion() string {
	information, readable := debug.ReadBuildInfo()
	if !readable || information.Main.Version == "" {
		return "devel"
	}
	return information.Main.Version
}

func toolDescriptors() []any {
	return []any{
		map[string]any{
			"name":        "remember",
			"description": "Keep facts worth recalling in a later conversation. Write each one as a whole sentence that stands on its own, naming who or what it is about instead of saying he, she or it, and keep one fact to a statement.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []any{"statements"},
				"properties": map[string]any{
					"statements": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "One fact per entry.",
					},
				},
			},
		},
		map[string]any{
			"name":        "search",
			"description": "Recall what is remembered about something. Returns an identifier and the fact on each line; the identifiers are what forget takes.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "What to recall, in the words you would use to ask."},
					"limit": map[string]any{"type": "integer", "description": "How many to return. Ten when omitted."},
				},
			},
		},
		map[string]any{
			"name":        "forget",
			"description": "Forget facts that are wrong or no longer wanted, by the identifiers search returned.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []any{"ids", "reason"},
				"properties": map[string]any{
					"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"reason": map[string]any{"type": "string", "description": "Why, kept alongside so the same fact is not learned again."},
				},
			},
		},
	}
}
