package bluememo_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
)

func TestEveryEnumIsDeclaredOnceAndTheSchemaFollowsIt(t *testing.T) {
	testFixture := newFixture(t)
	testFixture.store.Close()
	database, errorValue := sql.Open("sqlite", testFixture.path)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer database.Close()

	checks := map[string][]string{
		"cold_reason": stringsOf(bluememo.ColdReasons),
		"edge":        stringsOf(bluememo.Edges),
		"reason":      stringsOf(bluememo.TombstoneReasons),
	}
	for column, declared := range checks {
		constrained := checkedValues(t, database, column)
		if !slices.Equal(sorted(constrained), sorted(declared)) {
			t.Errorf("the CHECK on %s allows %v, the Go canon declares %v", column, constrained, declared)
		}
	}

	var schema map[string]any
	if errorValue := json.Unmarshal([]byte(bluememo.DecompositionSchemaDocument), &schema); errorValue != nil {
		t.Fatal(errorValue)
	}
	expiryEnum := schema["properties"].(map[string]any)["propositions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["expiry"].(map[string]any)["enum"]
	offered := []string{}
	for _, value := range expiryEnum.([]any) {
		offered = append(offered, value.(string))
	}
	if !slices.Equal(offered, stringsOf(bluememo.Expiries)) {
		t.Errorf("the decomposition schema offers %v, the Go canon declares %v", offered, bluememo.Expiries)
	}
	for _, expiry := range bluememo.Expiries {
		if !strings.Contains(bluememo.DecompositionInstruction, string(expiry)) {
			t.Errorf("the decomposition instruction never explains %s", expiry)
		}
	}
}

func checkedValues(t *testing.T, database *sql.DB, column string) []string {
	t.Helper()
	rows, errorValue := database.Query(`select sql from sqlite_master where type = 'table' and sql is not null`)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	defer rows.Close()
	pattern := regexp.MustCompile(`check \(` + column + ` in \(([^)]*)\)\)`)
	values := []string{}
	for rows.Next() {
		var statement string
		if errorValue := rows.Scan(&statement); errorValue != nil {
			t.Fatal(errorValue)
		}
		for _, match := range pattern.FindAllStringSubmatch(statement, -1) {
			for _, quoted := range strings.Split(match[1], ",") {
				values = append(values, strings.Trim(strings.TrimSpace(quoted), "'"))
			}
		}
	}
	if len(values) == 0 {
		t.Fatalf("no CHECK constrains %s", column)
	}
	return values
}

func stringsOf[Value ~string](values []Value) []string {
	converted := make([]string, len(values))
	for index, value := range values {
		converted[index] = string(value)
	}
	return converted
}

func sorted(values []string) []string {
	copied := slices.Clone(values)
	slices.Sort(copied)
	return copied
}

func TestTheDatabaseFileIsReadableOnlyByItsOwner(t *testing.T) {
	testFixture := newFixture(t)
	information, errorValue := os.Stat(testFixture.path)
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if permission := information.Mode().Perm(); permission != 0o600 {
		t.Fatalf("the memory database is %o, want 600", permission)
	}
}
