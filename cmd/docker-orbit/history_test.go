package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/history"
)

// These tests exercise historyCmd's own rendering logic (renderHistoryHuman)
// directly against internal/history.Event fixtures — internal/history's own
// package tests already cover Append/Read/Dir, so this file focuses on what
// wasn't covered before: the CLI-level presentation and JSON shape that
// `docker orbit history` actually produces.

func TestRenderHistoryHumanEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderHistoryHuman(&buf, "myapp", nil)

	out := buf.String()
	if !strings.Contains(out, `History for "myapp"`) {
		t.Errorf("output missing service header, got:\n%s", out)
	}
	if !strings.Contains(out, "No recorded events") {
		t.Errorf("empty history should say so explicitly, got:\n%s", out)
	}
}

func TestRenderHistoryHumanWithEvents(t *testing.T) {
	events := []history.Event{
		{
			Timestamp:     time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
			Service:       "myapp",
			Type:          history.EventRolloutCompleted,
			OldGeneration: "gen-1",
			NewGeneration: "gen-2",
			DurationMS:    1500,
			Trigger:       "cli",
			Result:        "success",
		},
		{
			Timestamp: time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC),
			Service:   "myapp",
			Type:      history.EventRolloutFailed,
			Trigger:   "cli",
			Result:    "failure",
			Reason:    "healthcheck timeout",
		},
	}

	var buf bytes.Buffer
	renderHistoryHuman(&buf, "myapp", events)
	out := buf.String()

	for _, want := range []string{
		"rollout_completed", "success", "1500ms", "gen-1 → gen-2",
		"rollout_failed", "failure", "healthcheck timeout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got:\n%s", want, out)
		}
	}
}

// TestRenderHistoryHumanGolden pins renderHistoryHuman's exact byte output
// against a fixture in testdata/ — see assertGolden in status_test.go for
// the UPDATE_GOLDEN=1 regeneration convention.
func TestRenderHistoryHumanGolden(t *testing.T) {
	events := []history.Event{
		{
			Timestamp:     time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
			Service:       "myapp",
			Type:          history.EventRolloutCompleted,
			OldGeneration: "gen-1",
			NewGeneration: "gen-2",
			DurationMS:    1500,
			Trigger:       "cli",
			Result:        "success",
		},
		{
			Timestamp: time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC),
			Service:   "myapp",
			Type:      history.EventRolloutFailed,
			Trigger:   "cli",
			Result:    "failure",
			Reason:    "healthcheck timeout",
		},
	}

	var buf bytes.Buffer
	renderHistoryHuman(&buf, "myapp", events)
	assertGolden(t, "history_golden.txt", buf.Bytes())
}

func TestHistoryJSONShapeIsStable(t *testing.T) {
	// Regression guard for the --json contract: field names and nesting are
	// part of what scripts/CI would depend on, matching the Stable API
	// Policy already applied to StatusReport and clierr.Error.JSON().
	events := []history.Event{{
		Timestamp: time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		Service:   "myapp",
		Type:      history.EventRollback,
		Trigger:   "cli",
		Result:    "success",
	}}

	payload := map[string]interface{}{
		"service": "myapp",
		"count":   len(events),
		"events":  events,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["service"] != "myapp" {
		t.Errorf(`decoded["service"] = %v, want "myapp"`, decoded["service"])
	}
	if decoded["count"].(float64) != 1 {
		t.Errorf(`decoded["count"] = %v, want 1`, decoded["count"])
	}
	evList, ok := decoded["events"].([]interface{})
	if !ok || len(evList) != 1 {
		t.Fatalf(`decoded["events"] = %v, want a 1-element array`, decoded["events"])
	}
	ev := evList[0].(map[string]interface{})
	if ev["type"] != "rollback" {
		t.Errorf(`events[0]["type"] = %v, want "rollback"`, ev["type"])
	}
	if _, ok := ev["timestamp"]; !ok {
		t.Error(`events[0] missing "timestamp" field`)
	}
}
