package main

import (
	"encoding/json"
	"os"
	"regexp"
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
