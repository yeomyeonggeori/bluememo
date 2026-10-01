package bluememo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const DecompositionInstruction = `You split text into the self-contained statements a memory store keeps. You do it in two passes.

First fill said. Read the text and list what it holds, in its own words, under each heading. This pass is reading, not writing: list every one you find, repeat nothing, and leave a heading empty when the text fills it with nothing.
  who    every person, group or organization the text names
  what   every thing, action or item named. A text naming a bowl and a cup lists both, never "pottery"
  when   every time the text gives, in its own wording, including how often: a date, a part of a day, a season, or a recurrence such as every morning
  where  every place
  why    every reason, purpose or cause given for something
  how    every manner, and everything a speaker says they felt or thought
  also   anything the text says that no heading above holds: a condition it depends on, who it was heard from, how much or how strongly. Empty when the headings hold everything

Then write propositions, the statements the store keeps. Nothing you listed in said may be missing from them, and nothing may appear that you did not list.

A statement is self-contained when it reads correctly on its own:
- A statement is never vaguer than the text it came from. Resolve every pronoun and every phrase standing in for something the text names: "I", "my", "there", "that company", "the city she grew up in". Keep the text's own word for a thing, its kind, its count and its day: "a sedan" written as "a car", or "three" written as "a few", loses what the statement would be found by. Leave general only what the text left general.
- A list keeps every item. Name them all in one statement, or write one statement each.
- A reason, a purpose or a cause stays with what it explains, in the same statement. Why someone took something up is asked about as often as that they did.
- What a speaker says they felt or thought is a statement of its own, in their own words. Never write a judgement the text does not make.
- Keep conditions, time, source and degree inside the statement.
- Something a person was told keeps its source in the same statement. Never split the source into a statement of its own.
- Write each statement in English. Keep every proper noun exactly as the text spells it, in its own script: a person, a place, a company, a product, a team or a title stays as written. "홍길동이 사과를 좋아한다" becomes "홍길동 likes apples".

isStatic: true for a lasting trait of the person, such as a name, a role, a team or a standing preference. An event, a one-off request or a passing situation is false. When unsure, false.

occurredOn: when an event happened, counted from today in the context below. A day is YYYY-MM-DD. A time of day is YYYY-MM-DDTHH:MM, read in the reader's own zone unless the text names another one, which is YYYY-MM-DDTHH:MM+HH:MM or a trailing Z. A longer time is its first and last moment joined by a slash. Empty when the statement is not an event, or when the text is too vague to place it.
Give the time at the precision the text gives, and never a narrower one. "last year" is that year's first and last day, "in June" that month's, "last quarter" that quarter's. A coarse time is still a time: never leave it out because it is not a single day. "a few weeks ago" does not place the event, so it stays empty.

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
Example text: I met Jordan yesterday. I always want meeting notes in Markdown. Answer me in English this quarter. I moved to Busan last year.
Example statements:
  isStatic false, occurredOn 2026-09-20: "Alex met Jordan."
  isStatic true: "Alex always wants meeting notes in Markdown."
  isStatic false, expiry end_of_quarter: "Alex wants answers in English."
  isStatic false, occurredOn 2025-01-01/2025-12-31: "Alex moved to Busan."

A wrong statement (never do this):
  Context: speaker Jordan Lee
  Text: I lead the payments team.
  Wrong: "Jordan leads the payments team." The name was cut short.
  Right: "Jordan Lee leads the payments team."
The speaker's name is exactly the string the context gives. Never cut it shorter.`

var stringList = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}

var DecompositionSchemaDocument = mustMarshalSchema(map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"said", "propositions"},
	"properties": map[string]any{
		"said": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"who", "what", "when", "where", "why", "how", "also"},
			"properties": map[string]any{
				"who":   stringList,
				"what":  stringList,
				"when":  stringList,
				"where": stringList,
				"why":   stringList,
				"how":   stringList,
				"also":  stringList,
			},
		},
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
