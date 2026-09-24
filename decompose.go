package bluememo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const DecompositionInstruction = `You split text into the self-contained statements a memory store keeps.

A statement is self-contained when it reads correctly on its own:
- Resolve every pronoun and omission. "I", "my", "he", "there" become the actual name or place.
- Keep conditions, time, source and degree inside the statement.
- Something a person was told keeps its source in the same statement. Never split the source into a statement of its own.
- Write each statement in the language the text is written in.

isStatic: true for a lasting trait of the person, such as a name, a role, a team or a standing preference. An event, a one-off request or a passing situation is false. When unsure, false.

occurredOn: the day an event happened (YYYY-MM-DD), counted from today in the context below. Empty when the statement is not an event.

expiry: when the text says the statement stops being true, pick that end.
  none            it has no end
  end_of_today    until the end of today
  end_of_week     until the end of this week
  end_of_month    until the end of this month
  end_of_quarter  until the end of this quarter
  end_of_year     until the end of this year
  on_date         the text names a date; put its last day in expiryDate (YYYY-MM-DD)
expiryDate is empty unless expiry is on_date. Pick the period; never compute a date yourself.

Do not:
- add anything the text does not say, or fill in what it leaves unsaid;
- turn the context below into statements; it only resolves pronouns and dates;
- respell, shorten or merge a name with the word that follows it;
- write a statement that only repeats a name ("Alex's name is Alex");
- keep small talk, acknowledgements or a request that is done once it is answered. When nothing is worth keeping, return an empty list.

Example context: speaker Alex, today 2026-09-21
Example text: I met Jordan yesterday. I always want meeting notes in Markdown. Answer me in English this quarter.
Example statements:
  isStatic false, occurredOn 2026-09-20: "Alex met Jordan."
  isStatic true: "Alex always wants meeting notes in Markdown."
  isStatic false, expiry end_of_quarter: "Alex wants answers in English."

A wrong statement (never do this):
  Context: speaker Jordan Lee
  Text: I lead the payments team.
  Wrong: "Jordan leads the payments team." The name was cut short.
  Right: "Jordan Lee leads the payments team."
The speaker's name is exactly the string the context gives. Never cut it shorter.`

var DecompositionSchemaDocument = mustMarshalSchema(map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"propositions"},
	"properties": map[string]any{
		"propositions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"content", "isStatic", "occurredOn", "expiry", "expiryDate"},
				"properties": map[string]any{
					"content":    map[string]any{"type": "string", "maxLength": ContentCharacterLimit},
					"isStatic":   map[string]any{"type": "boolean"},
					"occurredOn": map[string]any{"type": "string"},
					"expiry":     map[string]any{"type": "string", "enum": Expiries},
					"expiryDate": map[string]any{"type": "string"},
				},
			},
		},
	},
})

type decomposition struct {
	Propositions []Proposition `json:"propositions"`
}

func (store *Store) decompose(ctx context.Context, group pendingGroup) ([]Proposition, error) {
	response, errorValue := store.configuration.Model.GenerateStructured(ctx, StructuredRequest{
		SchemaName:     "memory_decomposition",
		SchemaDocument: DecompositionSchemaDocument,
		Instruction:    DecompositionInstruction,
		Subject:        decompositionSubject(group, store.configuration.Location),
	})
	if errorValue != nil {
		return nil, fmt.Errorf("decomposition model call failed: %w", errorValue)
	}
	var output decomposition
	if errorValue := json.Unmarshal([]byte(strings.TrimSpace(response)), &output); errorValue != nil {
		return nil, fmt.Errorf("decomposition output is not the schema: %w", errorValue)
	}
	return output.Propositions, nil
}

func decompositionSubject(group pendingGroup, location *time.Location) string {
	arrivedAt := group.arrivedAt.In(location)
	var subject strings.Builder
	fmt.Fprintf(&subject, "Context\nSpeaker: %s\nToday: %s (%s)\n\nText\n", group.speakerName, arrivedAt.Format(time.DateOnly), arrivedAt.Weekday())
	for _, note := range group.notes {
		if note.speakerName != "" && note.speakerName != group.speakerName {
			fmt.Fprintf(&subject, "%s: ", note.speakerName)
		}
		subject.WriteString(note.body)
		subject.WriteString("\n")
	}
	return subject.String()
}

func mustMarshalSchema(schema map[string]any) string {
	document, errorValue := json.Marshal(schema)
	if errorValue != nil {
		panic("schema does not marshal: " + errorValue.Error())
	}
	return string(document)
}
