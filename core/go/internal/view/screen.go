package view

import (
	"encoding/json"
	"strings"
)

// Scenes and screens (ADR-0060).
//
// A scene is the look a reply or a screen asks for: a mood, an accent and a
// layout, each from a closed list. A screen is a page of the owner's app that
// AURA designs: a scene plus blocks, where a widget block shows live data the
// client reads from the public API under its own token. Like a view, a screen
// is data, never code: the same sanitiser applies, and a widget names a
// source from a closed list, never a URL or a query.

// Scene is the sanitised look of a reply or screen. Empty fields mean "the
// client decides from context" (time of day, what is waiting).
type Scene struct {
	Mood   string `json:"mood,omitempty"`
	Accent string `json:"accent,omitempty"`
	Layout string `json:"layout,omitempty"`
}

var (
	Moods   = []string{"calm", "focus", "celebrate", "alert", "explore"}
	Accents = []string{"aurora", "ocean", "emerald", "amber", "rose", "violet"}
	Layouts = []string{"stack", "grid", "story"}
	// WidgetSources are the live-data blocks a screen may place.
	WidgetSources = []string{"greeting", "ask", "attention", "activity", "recent", "approvals", "notifications",
		"suggestions", "skills", "memories", "schedules", "limitations", "self", "usage"}
	widgetSources = set(WidgetSources)
)

func set(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func oneOf(v *string, allowed []string) string {
	s := strings.ToLower(strings.TrimSpace(deref(v)))
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return ""
}

// SanitizeScene keeps only known values; anything else is left to the client.
func SanitizeScene(raw json.RawMessage) Scene {
	var in struct {
		Mood   *string `json:"mood"`
		Accent *string `json:"accent"`
		Layout *string `json:"layout"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil {
		return Scene{}
	}
	return Scene{Mood: oneOf(in.Mood, Moods), Accent: oneOf(in.Accent, Accents), Layout: oneOf(in.Layout, Layouts)}
}

// SceneSchema is the JSON Schema of a model-proposed scene.
func SceneSchema() map[string]any {
	opt := func(xs []string) map[string]any {
		return map[string]any{"type": []string{"string", "null"}, "enum": append(append([]any{}, anys(xs)...), nil)}
	}
	return map[string]any{"type": []string{"object", "null"}, "additionalProperties": false, "required": []string{"mood", "accent", "layout"},
		"properties": map[string]any{"mood": opt(Moods), "accent": opt(Accents), "layout": opt(Layouts)}}
}

func anys(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// MaxScreenBlocks bounds a designed screen.
const MaxScreenBlocks = 16

// Screen is a sanitised, designed page.
type Screen struct {
	Scene  Scene   `json:"scene"`
	Blocks []Block `json:"blocks"`
}

// SanitizeScreen parses a model-proposed screen. Links are dropped (a screen
// has no evidence to give them provenance); widgets are allowed.
func SanitizeScreen(scene, blocks json.RawMessage) (Screen, []string) {
	bs, notes := Sanitize(blocks, Options{Widgets: true, MaxBlocks: MaxScreenBlocks})
	if bs == nil {
		bs = []Block{}
	}
	return Screen{Scene: SanitizeScene(scene), Blocks: bs}, notes
}

// ScreenSchema is the JSON Schema of the blocks of a model-proposed screen:
// the view schema plus the widget type and its fields.
func ScreenSchema() map[string]any {
	s := Schema()
	block := s["items"].(map[string]any)
	props := block["properties"].(map[string]any)
	props["type"] = map[string]any{"type": "string", "enum": append(append([]string{}, Types...), "widget")}
	// The source is not an enum here: one mistyped source should cost the
	// design that block (the sanitiser drops it), not the whole answer.
	props["source"] = map[string]any{"type": []string{"string", "null"}}
	props["limit"] = map[string]any{"type": []string{"integer", "null"}}
	block["required"] = append(append([]string{}, block["required"].([]string)...), "source", "limit")
	return s
}
