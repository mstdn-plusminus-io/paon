package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type mastodonSettingUpdate struct {
	key   string
	value any
}

// Ruby's Oj.dump preserves a Hash's insertion order. Preserve that order and
// untouched JSON values when changing top-level settings: decoding everything
// into map[string]any sorts keys on output and rounds integers above 2^53.
func rewriteMastodonSettings(source string, updates []mastodonSettingUpdate) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(source))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("settings must be a JSON object")
	}
	type entry struct {
		key   string
		value json.RawMessage
	}
	var entries []entry
	positions := map[string]int{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("settings key must be a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if position, exists := positions[key]; exists {
			entries[position].value = value
		} else {
			positions[key] = len(entries)
			entries = append(entries, entry{key, value})
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("settings contain trailing JSON data")
	}
	for _, update := range updates {
		encoded, err := encodeMastodonSettingValue(update.value)
		if err != nil {
			return nil, err
		}
		if position, exists := positions[update.key]; exists {
			entries[position].value = encoded
		} else {
			positions[update.key] = len(entries)
			entries = append(entries, entry{update.key, encoded})
		}
	}
	var result bytes.Buffer
	result.WriteByte('{')
	for index, entry := range entries {
		if index != 0 {
			result.WriteByte(',')
		}
		key, err := encodeMastodonSettingValue(entry.key)
		if err != nil {
			return nil, err
		}
		result.Write(key)
		result.WriteByte(':')
		if err := json.Compact(&result, entry.value); err != nil {
			return nil, err
		}
	}
	result.WriteByte('}')
	return result.Bytes(), nil
}

func encodeMastodonSettingValue(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}
