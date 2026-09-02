package bluememo

import (
	"strings"
	"testing"
	"time"
)

func validFact() Fact {
	return Fact{
		FactID:        "fact-1",
		EpisodeID:     "episode-1",
		ScopeType:     ScopeTypePrivate,
		OwnerPersonID: "person-1",
		Kind:          FactKindFact,
		Content:       "이샘플 owns the Q3 review",
		ValidFrom:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

func circleFact(circleIDs ...string) Fact {
	fact := validFact()
	fact.ScopeType, fact.OwnerPersonID, fact.CircleIDs = ScopeTypeCircle, "", circleIDs
	return fact
}

func workspaceFact() Fact {
	fact := validFact()
	fact.ScopeType, fact.OwnerPersonID = ScopeTypeWorkspace, ""
	return fact
}

func TestValidateFactAcceptsEveryScopeShape(t *testing.T) {
	for name, fact := range map[string]Fact{"private": validFact(), "circle": circleFact("platform", "data"), "workspace": workspaceFact()} {
		if errorValue := ValidateFact(fact); errorValue != nil {
			t.Fatalf("expected the %s fact to validate, got %v", name, errorValue)
		}
	}
}

func TestValidateFactRejectsEachBrokenField(t *testing.T) {
	cases := map[string]func(*Fact){
		"missing id":                  func(fact *Fact) { fact.FactID = " " },
		"unknown scope":               func(fact *Fact) { fact.ScopeType = "conversation" },
		"private without owner":       func(fact *Fact) { fact.OwnerPersonID = "" },
		"private with circles":        func(fact *Fact) { fact.CircleIDs = []string{"platform"} },
		"circle without circles":      func(fact *Fact) { *fact = circleFact(" ") },
		"circle with owner":           func(fact *Fact) { *fact = circleFact("platform"); fact.OwnerPersonID = "person-1" },
		"workspace with circles":      func(fact *Fact) { *fact = workspaceFact(); fact.CircleIDs = []string{"platform"} },
		"unknown kind":                func(fact *Fact) { fact.Kind = "rumour" },
		"empty content":               func(fact *Fact) { fact.Content = "  " },
		"oversized content":           func(fact *Fact) { fact.Content = strings.Repeat("가", FactContentCharacterLimit+1) },
		"missing valid_from":          func(fact *Fact) { fact.ValidFrom = time.Time{} },
		"temporary without expiry":    func(fact *Fact) { fact.Kind = FactKindTemporary },
		"durable fact with an expiry": func(fact *Fact) { fact.ValidUntil = fact.ValidFrom.Add(time.Hour) },
	}
	for name, mutate := range cases {
		fact := validFact()
		mutate(&fact)
		if errorValue := ValidateFact(fact); errorValue == nil {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
}

func TestFactIsLiveHonoursSupersedeForgetAndExpiry(t *testing.T) {
	referenceTime := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if !validFact().IsLive(referenceTime) {
		t.Fatal("expected an untouched fact to be live")
	}
	superseded := validFact()
	superseded.SupersededBy = "fact-2"
	forgotten := validFact()
	forgotten.ForgottenAt = referenceTime
	expired := validFact()
	expired.ValidUntil = referenceTime.Add(-time.Minute)
	for name, fact := range map[string]Fact{"superseded": superseded, "forgotten": forgotten, "expired": expired} {
		if fact.IsLive(referenceTime) {
			t.Fatalf("expected the %s fact to be dead", name)
		}
	}
}

func TestNormalizeCircleIDsLowercasesDeduplicatesAndSorts(t *testing.T) {
	normalized := NormalizeCircleIDs([]string{" Platform ", "data", "platform", "", "Data"})
	if strings.Join(normalized, ",") != "data,platform" {
		t.Fatalf("expected data,platform, got %v", normalized)
	}
}

func TestReadableCirclesFollowsContainmentTransitivelyAndSafely(t *testing.T) {
	contained := map[string][]string{
		"company":     {"engineering", "sales"},
		"engineering": {"platform", "data"},
		"platform":    {"engineering"},
	}
	readable := ReadableCircles([]string{"engineering"}, contained)
	if strings.Join(readable, ",") != "data,engineering,platform" {
		t.Fatalf("expected engineering and what it contains, got %v", readable)
	}
	if strings.Join(ReadableCircles([]string{"platform"}, contained), ",") != "data,engineering,platform" {
		t.Fatal("expected a cycle to resolve without looping")
	}
	if strings.Join(ReadableCircles([]string{"sales"}, contained), ",") != "sales" {
		t.Fatal("expected a leaf circle to read only itself")
	}
}

func TestReaderCanReadAppliesScopeContainmentRankAndClasses(t *testing.T) {
	reader := NewReader("person-1", []string{"engineering"}, map[string][]string{"engineering": {"platform"}}, 1, []string{"finance"})
	ownPrivate := validFact()
	otherPrivate := validFact()
	otherPrivate.OwnerPersonID = "person-2"
	tooSecret := workspaceFact()
	tooSecret.SecurityLevelRank = 2
	wrongClass := workspaceFact()
	wrongClass.RequiredClasses = []string{"legal"}
	rightClass := workspaceFact()
	rightClass.RequiredClasses = []string{"finance"}
	for name, expectation := range map[string]struct {
		fact    Fact
		canRead bool
	}{
		"own private":            {ownPrivate, true},
		"other private":          {otherPrivate, false},
		"member circle":          {circleFact("engineering"), true},
		"contained circle":       {circleFact("platform"), true},
		"one of several circles": {circleFact("sales", "platform"), true},
		"stranger circle":        {circleFact("sales"), false},
		"containing circle":      {circleFact("company"), false},
		"workspace":              {workspaceFact(), true},
		"above clearance":        {tooSecret, false},
		"missing class":          {wrongClass, false},
		"granted class":          {rightClass, true},
	} {
		if reader.CanRead(expectation.fact) != expectation.canRead {
			t.Fatalf("expected %s readable=%v", name, expectation.canRead)
		}
	}
	if !reader.CanWriteCircle("Engineering") || reader.CanWriteCircle("platform") {
		t.Fatal("expected writes to be limited to circles the person is a member of")
	}
}

func TestValidateEmbeddingRequiresTheStoredDimensionCount(t *testing.T) {
	if errorValue := ValidateEmbedding(make([]float32, EmbeddingDimensionCount)); errorValue != nil {
		t.Fatal(errorValue)
	}
	if errorValue := ValidateEmbedding(make([]float32, 768)); errorValue == nil {
		t.Fatal("expected a 768-dimensional embedding to be rejected")
	}
}
