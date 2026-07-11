// This file is the bench package's single serialization surface for a Report:
// EncodeReport/DecodeReport round-trip the whole scorecard as JSON, and the
// GateOutcome (de)serializes as its human-readable String() form so a saved
// report is diffable across runs. bench's -json flag encodes, diff decodes.
package bench

import (
	"encoding/json"
	"fmt"
	"io"
)

// EncodeReport writes r as indented JSON plus a trailing newline (matching the
// capture/label manifest style). It is the single serialization surface: bench's
// -json flag calls it, diff round-trips through DecodeReport.
func EncodeReport(w io.Writer, r Report) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: encode report: %w", err)
	}
	out = append(out, '\n')
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("bench: write report: %w", err)
	}
	return nil
}

// DecodeReport reads one JSON Report (as written by EncodeReport) from r.
func DecodeReport(r io.Reader) (Report, error) {
	rep := Report{}
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return Report{}, fmt.Errorf("bench: decode report: %w", err)
	}
	return rep, nil
}

// MarshalJSON encodes a GateOutcome as its String() form ("reject",
// "certain-accept", "uncertain") so Violation.Want/Got are human-readable and
// stable across runs.
func (o GateOutcome) MarshalJSON() ([]byte, error) {
	return json.Marshal(o.String())
}

// UnmarshalJSON decodes a GateOutcome from its String() form, erroring on any
// unrecognized string so a corrupt report fails loudly rather than silently
// reading as GateReject.
func (o *GateOutcome) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("bench: decode gate outcome: %w", err)
	}
	outcome, ok := gateOutcomeFromString(s)
	if !ok {
		return fmt.Errorf("bench: unknown gate outcome %q", s)
	}
	*o = outcome
	return nil
}

// gateOutcomeFromString is the inverse of GateOutcome.String: it maps a rendered
// outcome back to its constant, reporting ok=false for any unknown string.
func gateOutcomeFromString(s string) (GateOutcome, bool) {
	switch s {
	case "reject":
		return GateReject, true
	case "certain-accept":
		return GateCertainAccept, true
	case "uncertain":
		return GateUncertain, true
	default:
		return 0, false
	}
}
