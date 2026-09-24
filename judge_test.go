package bluememo_test

import (
	"context"
	"testing"

	"github.com/yeomyeonggeori/bluememo"
	"github.com/yeomyeonggeori/bluememo/bluememotest"
)

func judgeWith(relation map[string]float64, target map[string]float64) bluememo.DistributionJudge {
	return bluememo.DistributionJudge{Chooser: bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
		bluememo.RelationInstruction:   relation,
		bluememo.ImportanceInstruction: {"4": 0.9},
		bluememo.TargetInstruction:     target,
	}}}
}

func TestTheJudgeHoldsSameToAHigherMarginThanTheOtherBranches(t *testing.T) {
	cases := []struct {
		name     string
		relation map[string]float64
		want     bluememo.Relation
	}{
		{"confident same", map[string]float64{"1": 0.6, "3": 0.3}, bluememo.RelationSame},
		{"hesitant same", map[string]float64{"1": 0.45, "3": 0.3}, bluememo.RelationUnrelated},
		{"hesitant extends", map[string]float64{"3": 0.45, "1": 0.3}, bluememo.RelationExtends},
		{"coin-flip updates", map[string]float64{"2": 0.41, "3": 0.38}, bluememo.RelationUnrelated},
	}
	for _, testCase := range cases {
		judgement, errorValue := judgeWith(testCase.relation, map[string]float64{"0": 0.9}).Judge(context.Background(), "새 명제", []string{"held statement"})
		if errorValue != nil {
			t.Fatal(errorValue)
		}
		if judgement.Relation != testCase.want || judgement.Importance != 4 {
			t.Errorf("%s: got %+v, want %s with importance 4", testCase.name, judgement, testCase.want)
		}
	}
}

func TestNoiseIsARatingAndAFirstMemoryIsNeverAskedForARelation(t *testing.T) {
	chatter := bluememo.DistributionJudge{Chooser: bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
		bluememo.ImportanceInstruction: {"1": 0.8, "2": 0.1},
	}}}
	judgement, errorValue := chatter.Judge(context.Background(), "ok", []string{"held statement"})
	if errorValue != nil || judgement.Relation != bluememo.RelationNoise {
		t.Fatalf("a confident rating of 1 should be noise, got %+v (%v)", judgement, errorValue)
	}

	firstMemory := bluememo.DistributionJudge{Chooser: bluememotest.ScriptedChooser{Distributions: map[string]map[string]float64{
		bluememo.ImportanceInstruction: {"4": 0.9},
		bluememo.RelationInstruction:   {"1": 0.99},
	}}}
	judgement, errorValue = firstMemory.Judge(context.Background(), "Alex leads the payments team.", nil)
	if errorValue != nil || judgement.Relation != bluememo.RelationUnrelated || judgement.Importance != 4 {
		t.Fatalf("with nothing held the judge should keep the statement without asking, got %+v (%v)", judgement, errorValue)
	}
}

func TestTheJudgeKeepsApartWhenItNamesNoTarget(t *testing.T) {
	judgement, errorValue := judgeWith(map[string]float64{"2": 0.9}, map[string]float64{"9": 0.8}).Judge(context.Background(), "새 명제", []string{"held statement"})
	if errorValue != nil {
		t.Fatal(errorValue)
	}
	if judgement.Relation != bluememo.RelationUnrelated || judgement.TargetIndex != -1 {
		t.Fatalf("no target should mean unrelated, got %+v", judgement)
	}
}
