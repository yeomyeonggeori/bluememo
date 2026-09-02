package bluememo

import "strings"

const (
	transcriptStepCharacterLimit  = 1200
	transcriptTotalCharacterLimit = 12000
)

type TranscriptStep struct {
	Instruction string
	Status      string
	Output      string
}

type Transcript struct {
	Prompt        string
	Steps         []TranscriptStep
	Result        string
	Outcome       string
	FailureReason string
}

func RenderTranscript(transcript Transcript) string {
	sections := []string{"Request:\n" + strings.TrimSpace(transcript.Prompt)}
	for _, step := range transcript.Steps {
		instruction := strings.TrimSpace(step.Instruction)
		output := strings.TrimSpace(step.Output)
		if instruction == "" && output == "" {
			continue
		}
		section := "Step (" + step.Status + "): " + clampRunes(instruction, transcriptStepCharacterLimit)
		if output != "" {
			section += "\nOutput: " + clampRunes(output, transcriptStepCharacterLimit)
		}
		sections = append(sections, section)
	}
	if result := strings.TrimSpace(transcript.Result); result != "" {
		sections = append(sections, "Final reply:\n"+result)
	}
	if failureReason := strings.TrimSpace(transcript.FailureReason); failureReason != "" {
		sections = append(sections, "Outcome: "+transcript.Outcome+" ("+failureReason+")")
	} else {
		sections = append(sections, "Outcome: "+transcript.Outcome)
	}
	return clampRunes(strings.Join(sections, "\n\n"), transcriptTotalCharacterLimit)
}

func clampRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + " …"
}
