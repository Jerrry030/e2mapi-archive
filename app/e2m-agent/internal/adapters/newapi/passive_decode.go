package newapi

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON accepts the two NewAPI log encodings seen across versions:
// other is normally a JSON string, while some compatible deployments expose
// the same value as an already-decoded object.
func (r *logRecord) UnmarshalJSON(raw []byte) error {
	type recordAlias logRecord
	var wire struct {
		recordAlias
		Other json.RawMessage `json:"other"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*r = logRecord(wire.recordAlias)
	other := bytes.TrimSpace(wire.Other)
	if len(other) == 0 || bytes.Equal(other, []byte("null")) {
		return nil
	}
	if other[0] == '"' {
		var text string
		if err := json.Unmarshal(other, &text); err != nil {
			return err
		}
		r.Other = json.RawMessage(text)
		return nil
	}
	r.Other = append(json.RawMessage(nil), other...)
	return nil
}
