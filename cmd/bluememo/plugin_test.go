package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The manifest is a closed object: one stray or mistyped key is a schema
// violation for the whole plugin, and the schema that says so lives on the
// web, so nothing here would catch it.
//
// https://agent-plugins.org/schemas/1.0.0/plugin.schema.json
func TestTheManifestStaysWithinThePortableFields(t *testing.T) {
	allowed := map[string]bool{
		"$schema": true, "name": true, "version": true, "description": true,
		"author": true, "homepage": true, "repository": true, "license": true,
		"keywords": true, "extensions": true,
	}
	manifest := readObject(t, "../../plugin.json")
	for field := range manifest {
		if !allowed[field] {
			t.Errorf("%q is not a portable manifest field", field)
		}
	}
	for _, required := range []string{"$schema", "name"} {
		if _, carried := manifest[required]; !carried {
			t.Errorf("the manifest must carry %q", required)
		}
	}
	name, named := manifest["name"].(string)
	if !named || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`).MatchString(name) {
		t.Errorf("a plugin name is lowercase and starts and ends alphanumeric, got %v", manifest["name"])
	}
}

// A client runs what mcp.json names. If the command or the word it is given
// drifts from what this module builds, the plugin keeps installing and stops
// working.
func TestTheDeclaredServerIsTheCommandThisModuleBuilds(t *testing.T) {
	declaration := readObject(t, "../../mcp.json")
	servers, declared := declaration["mcpServers"].(map[string]any)
	if !declared {
		t.Fatal("mcp.json declares no servers")
	}
	server, declared := servers["bluememo"].(map[string]any)
	if !declared {
		t.Fatal("mcp.json declares no bluememo server")
	}
	if server["type"] != "stdio" {
		t.Errorf("expected a stdio server, got %v", server["type"])
	}
	if server["command"] != "bluememo" {
		t.Errorf("expected the command this module builds, got %v", server["command"])
	}
	arguments, passed := server["args"].([]any)
	if !passed || len(arguments) != 1 || arguments[0] != serveCommand {
		t.Errorf("expected the server to be started with %q, got %v", serveCommand, server["args"])
	}
}

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	document, errorValue := os.ReadFile(path)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	var object map[string]any
	if errorValue := json.Unmarshal(document, &object); errorValue != nil {
		t.Fatal(errorValue)
	}
	return object
}

// A skill whose frontmatter breaks the Agent Skills specification is skipped
// by a conformant client, which loads everything else and says nothing the
// user is likely to read.
//
// https://agentskills.io/specification
func TestEverySkillConformsToTheSkillSpecification(t *testing.T) {
	allowed := map[string]bool{
		"name": true, "description": true, "license": true,
		"compatibility": true, "metadata": true, "allowed-tools": true,
	}
	named := regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

	directories, errorValue := os.ReadDir("../../skills")
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	found := 0
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		document, errorValue := os.ReadFile(filepath.Join("../../skills", directory.Name(), "SKILL.md"))
		if errorValue != nil {
			continue
		}
		found++
		frontmatter := readFrontmatter(t, directory.Name(), string(document))
		for field := range frontmatter {
			if !allowed[field] {
				t.Errorf("%s: %q is not a skill frontmatter field", directory.Name(), field)
			}
		}
		name := frontmatter["name"]
		if name != directory.Name() {
			t.Errorf("%s: the name must match the directory, got %q", directory.Name(), name)
		}
		if len(name) > 64 || !named.MatchString(name) {
			t.Errorf("%s: a skill name is lowercase alphanumeric with single hyphens between, got %q", directory.Name(), name)
		}
		description := frontmatter["description"]
		if description == "" || len(description) > 1024 {
			t.Errorf("%s: a description is present and at most 1024 characters, got %d", directory.Name(), len(description))
		}
		if compatibility := frontmatter["compatibility"]; len(compatibility) > 500 {
			t.Errorf("%s: compatibility is at most 500 characters, got %d", directory.Name(), len(compatibility))
		}
	}
	if found == 0 {
		t.Fatal("expected at least one skill under skills/")
	}
}

// readFrontmatter reads the top-level keys of a SKILL.md, which is all the
// fields checked here are. A nested value is recorded as present and empty.
func readFrontmatter(t *testing.T, skill string, document string) map[string]string {
	t.Helper()
	lines := strings.Split(document, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("%s: SKILL.md must open with YAML frontmatter", skill)
	}
	fields := map[string]string{}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return fields
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, split := strings.Cut(line, ":")
		if !split {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	t.Fatalf("%s: SKILL.md frontmatter is never closed", skill)
	return nil
}
