package env_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/env"
)

func TestEnvOr(t *testing.T) {
	t.Run("returns the value when set", func(t *testing.T) {
		t.Setenv("KNOB", "value")
		if got := env.EnvOr("KNOB", "fallback"); got != "value" {
			t.Errorf("EnvOr() = %q, want %q", got, "value")
		}
	})

	t.Run("falls back when unset", func(t *testing.T) {
		if got := env.EnvOr("UNSET_KNOB", "fallback"); got != "fallback" {
			t.Errorf("EnvOr() = %q, want %q", got, "fallback")
		}
	})

	// An exported-but-blank variable is the mistake it usually is, not a request
	// for the empty string.
	t.Run("falls back when set but empty", func(t *testing.T) {
		t.Setenv("KNOB", "")
		if got := env.EnvOr("KNOB", "fallback"); got != "fallback" {
			t.Errorf("EnvOr() = %q, want %q", got, "fallback")
		}
	})
}

func TestLoaderAccessors(t *testing.T) {
	// Each case sets one variable and reads it back through the accessor under
	// test: want is the resolved value, wantErr whether the knob was rejected.
	tests := []struct {
		name    string
		set     string // raw value; "" means leave the variable unset
		read    func(l *env.Loader) any
		want    any
		wantErr bool
	}{
		{
			name: "Duration parses",
			set:  "90s",
			read: func(l *env.Loader) any { return l.Duration("KNOB", time.Minute) },
			want: 90 * time.Second,
		},
		{
			name: "Duration falls back when unset",
			read: func(l *env.Loader) any { return l.Duration("KNOB", time.Minute) },
			want: time.Minute,
		},
		{
			name:    "Duration rejects a bare number",
			set:     "30",
			read:    func(l *env.Loader) any { return l.Duration("KNOB", time.Minute) },
			want:    time.Minute,
			wantErr: true,
		},
		{
			// Duration deliberately permits zero; PositiveDuration is the strict one.
			name: "Duration allows zero",
			set:  "0s",
			read: func(l *env.Loader) any { return l.Duration("KNOB", time.Minute) },
			want: time.Duration(0),
		},
		{
			name:    "PositiveDuration rejects zero",
			set:     "0s",
			read:    func(l *env.Loader) any { return l.PositiveDuration("KNOB", time.Minute) },
			want:    time.Minute,
			wantErr: true,
		},
		{
			name:    "PositiveDuration rejects a negative",
			set:     "-5m",
			read:    func(l *env.Loader) any { return l.PositiveDuration("KNOB", time.Minute) },
			want:    time.Minute,
			wantErr: true,
		},
		{
			name: "PositiveInt parses",
			set:  "42",
			read: func(l *env.Loader) any { return l.PositiveInt("KNOB", 7) },
			want: 42,
		},
		{
			name:    "PositiveInt rejects zero",
			set:     "0",
			read:    func(l *env.Loader) any { return l.PositiveInt("KNOB", 7) },
			want:    7,
			wantErr: true,
		},
		{
			name:    "PositiveInt rejects a non-number",
			set:     "lots",
			read:    func(l *env.Loader) any { return l.PositiveInt("KNOB", 7) },
			want:    7,
			wantErr: true,
		},
		{
			name: "Bool parses false",
			set:  "false",
			read: func(l *env.Loader) any { return l.Bool("KNOB", true) },
			want: false,
		},
		{
			name: "Bool accepts 0",
			set:  "0",
			read: func(l *env.Loader) any { return l.Bool("KNOB", true) },
			want: false,
		},
		{
			// A kill switch misspelled "yes" must fail loudly, not stay armed.
			name:    "Bool rejects yes",
			set:     "yes",
			read:    func(l *env.Loader) any { return l.Bool("KNOB", true) },
			want:    true,
			wantErr: true,
		},
		{
			name: "Fraction parses",
			set:  "0.25",
			read: func(l *env.Loader) any { return l.Fraction("KNOB", 0.01) },
			want: 0.25,
		},
		{
			name: "Fraction allows both ends of the interval",
			set:  "1",
			read: func(l *env.Loader) any { return l.Fraction("KNOB", 0.01) },
			want: 1.0,
		},
		{
			name: "Fraction allows zero",
			set:  "0",
			read: func(l *env.Loader) any { return l.Fraction("KNOB", 0.01) },
			want: 0.0,
		},
		{
			name:    "Fraction rejects above one",
			set:     "1.5",
			read:    func(l *env.Loader) any { return l.Fraction("KNOB", 0.01) },
			want:    0.01,
			wantErr: true,
		},
		{
			name:    "Fraction rejects a negative",
			set:     "-0.1",
			read:    func(l *env.Loader) any { return l.Fraction("KNOB", 0.01) },
			want:    0.01,
			wantErr: true,
		},
		{
			name: "String falls back when unset",
			read: func(l *env.Loader) any { return l.String("KNOB", "fallback") },
			want: "fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set != "" {
				t.Setenv("KNOB", tt.set)
			}
			var l env.Loader
			got := tt.read(&l)
			if got != tt.want {
				t.Errorf("resolved %v, want %v", got, tt.want)
			}
			if err := l.Err(); (err != nil) != tt.wantErr {
				t.Errorf("Err() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoaderText(t *testing.T) {
	t.Run("applies the fallback when unset", func(t *testing.T) {
		var l env.Loader
		var level slog.LevelVar
		l.Text("LOG_LEVEL", &level, "info")
		if err := l.Err(); err != nil {
			t.Fatalf("Err() = %v, want nil", err)
		}
		if level.Level() != slog.LevelInfo {
			t.Errorf("level = %v, want %v", level.Level(), slog.LevelInfo)
		}
	})

	t.Run("parses the environment value", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "debug")
		var l env.Loader
		var level slog.LevelVar
		l.Text("LOG_LEVEL", &level, "info")
		if err := l.Err(); err != nil {
			t.Fatalf("Err() = %v, want nil", err)
		}
		if level.Level() != slog.LevelDebug {
			t.Errorf("level = %v, want %v", level.Level(), slog.LevelDebug)
		}
	})

	t.Run("rejects a value the destination refuses", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "chatty")
		var l env.Loader
		var level slog.LevelVar
		l.Text("LOG_LEVEL", &level, "info")
		err := l.Err()
		if err == nil {
			t.Fatal("Err() = nil, want an error")
		}
		if !strings.Contains(err.Error(), "LOG_LEVEL") {
			t.Errorf("Err() = %q, want it to name LOG_LEVEL", err)
		}
	})
}

// The point of the Loader: an environment with several mistakes in it surfaces
// all of them on one start, not one per restart.
func TestLoaderReportsEveryFailureAtOnce(t *testing.T) {
	t.Setenv("WORKERS", "none")
	t.Setenv("INTERVAL", "soon")
	t.Setenv("ENABLED", "maybe")
	t.Setenv("GOOD", "8")

	var l env.Loader
	l.PositiveInt("WORKERS", 4)
	l.PositiveDuration("INTERVAL", 24*time.Hour)
	l.Bool("ENABLED", true)
	if got := l.PositiveInt("GOOD", 4); got != 8 {
		t.Errorf("GOOD = %d, want 8 -- a valid knob must survive its neighbours", got)
	}

	err := l.Err()
	if err == nil {
		t.Fatal("Err() = nil, want three errors")
	}
	for _, key := range []string{"WORKERS", "INTERVAL", "ENABLED"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Err() = %q, want it to name %s", err, key)
		}
	}
	if strings.Contains(err.Error(), "GOOD") {
		t.Errorf("Err() = %q, want no mention of the valid knob", err)
	}
}

// A rejection has to carry enough to fix the container's environment without
// opening this source: the key, the offending value, and the default it took.
func TestLoaderErrorNamesValueAndDefault(t *testing.T) {
	t.Setenv("CRAWL_MAX_WORKERS", "-3")

	var l env.Loader
	l.PositiveInt("CRAWL_MAX_WORKERS", 16)

	err := l.Err()
	if err == nil {
		t.Fatal("Err() = nil, want an error")
	}
	for _, want := range []string{"CRAWL_MAX_WORKERS", `"-3"`, "16"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Err() = %q, want it to contain %s", err, want)
		}
	}
}

func TestLoaderErrIsNilWhenEverythingResolves(t *testing.T) {
	var l env.Loader
	l.PositiveInt("UNSET_INT", 1)
	l.Duration("UNSET_DURATION", time.Second)
	l.Bool("UNSET_BOOL", true)
	l.Fraction("UNSET_FRACTION", 0.5)
	l.String("UNSET_STRING", "x")
	if err := l.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}
