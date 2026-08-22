package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestLoadGateConfig(t *testing.T) {
	t.Run("empty path returns the unchanged default", func(t *testing.T) {
		got, err := loadGateConfig("")
		if err != nil {
			t.Fatalf("loadGateConfig: %v", err)
		}
		if !reflect.DeepEqual(got, crawler.DefaultLLMGateConfig()) {
			t.Errorf("loadGateConfig(\"\") = %+v, want DefaultLLMGateConfig", got)
		}
	})

	t.Run("partial override changes only its named field and keeps the seeded scoring floats", func(t *testing.T) {
		def := crawler.DefaultLLMGateConfig()
		if def.CertainThreshold == 0 {
			t.Fatalf("test assumes a non-zero default CertainThreshold, got %v", def.CertainThreshold)
		}

		path := filepath.Join(t.TempDir(), "override.json")
		// Override only RejectThreshold; CertainThreshold and the weights are absent.
		if err := os.WriteFile(path, []byte(`{"RejectThreshold": 0.9}`), 0o600); err != nil {
			t.Fatalf("write override: %v", err)
		}

		got, err := loadGateConfig(path)
		if err != nil {
			t.Fatalf("loadGateConfig: %v", err)
		}
		if got.RejectThreshold != 0.9 {
			t.Errorf("RejectThreshold = %v, want 0.9 (override not applied)", got.RejectThreshold)
		}
		// The unnamed field must keep its default, not fall to a zero value that
		// would make the final rung certain-accept every page.
		if got.CertainThreshold != def.CertainThreshold {
			t.Errorf("CertainThreshold = %v, want default %v (override zeroed an unnamed field)", got.CertainThreshold, def.CertainThreshold)
		}
	})

	t.Run("the Learned Veto is reachable by field name, so the scorecard needs no second path", func(t *testing.T) {
		// This is what lets `score-capture -gate-config` score the shipped weights with
		// ADR-0049's rung on without a second scoring path. LLMGateConfig carries no json
		// tags, so the JSON key IS the Go field name: renaming the field would silently
		// break the command the Extract Gold Set README documents, and this case is what
		// turns that into a failure.
		path := filepath.Join(t.TempDir(), "veto.json")
		if err := os.WriteFile(path, []byte(`{"LearnedVeto": true}`), 0o600); err != nil {
			t.Fatalf("write override: %v", err)
		}

		got, err := loadGateConfig(path)
		if err != nil {
			t.Fatalf("loadGateConfig: %v", err)
		}
		if !got.LearnedVeto {
			t.Error("LearnedVeto = false after an override naming it, want true")
		}
		// The veto only ever judges pages the Positive Evidence rung admitted, so an
		// override that zeroed that rung would score a configuration nobody ships.
		if !got.RequirePositiveEvidence {
			t.Error("RequirePositiveEvidence = false, want the default true: naming the veto must not clear the rung it sits on top of")
		}
	})
}
