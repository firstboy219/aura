package tools

import (
	"context"
	"errors"
	"testing"
)

type stubSearcher struct {
	res []SearchResult
	err error
}

func (s stubSearcher) Name() string { return "stub" }
func (s stubSearcher) Search(context.Context, string, int) ([]SearchResult, error) {
	return s.res, s.err
}

func TestMultiMergesProvidersAndSurvivesOneFailure(t *testing.T) {
	a := stubSearcher{res: []SearchResult{{Title: "a1"}, {Title: "a2"}}}
	b := stubSearcher{res: []SearchResult{{Title: "b1"}}}
	got, err := Multi{a, b}.Search(context.Background(), "q", 3)
	if err != nil || len(got) != 3 || got[0].Title != "a1" || got[1].Title != "b1" || got[2].Title != "a2" {
		t.Fatalf("%v %+v", err, got)
	}
	got, err = Multi{stubSearcher{err: errors.New("down")}, b}.Search(context.Background(), "q", 3)
	if err != nil || len(got) != 1 {
		t.Fatalf("one failure must not fail the search: %v %+v", err, got)
	}
	if _, err := (Multi{stubSearcher{err: errors.New("down")}}).Search(context.Background(), "q", 3); !errors.Is(err, ErrSearch) {
		t.Fatalf("all down: %v", err)
	}
}
