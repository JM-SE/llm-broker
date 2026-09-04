package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Defaults are applied after decoding, before validation. They are part of
// the product contract.
const (
	DefaultListen                       = "127.0.0.1:8010"
	DefaultBodyLimitBytes               = 512 * 1024
	DefaultMaxConnections               = 10
	DefaultConnectTimeout               = 10 // seconds
	DefaultMaxActiveTasks               = 250
	DefaultMaxOutputTokens              = 8192
	DefaultResponseHeaderTimeoutSeconds = 15
)

// LoadFile reads, decodes, and cross-validates a configuration file.
func LoadFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg, err := Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Decode strictly decodes configuration JSON, applies defaults, and
// cross-validates. Unknown fields, duplicate fields, and trailing data are
// rejected.
func Decode(r io.Reader) (*Config, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if err := rejectDuplicateFields(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		var ue *json.UnmarshalTypeError
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("field %s: wrong type", jsonPath(ue.Field))
		}
		return nil, fmt.Errorf("decode: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode: unexpected trailing content")
		}
		return nil, fmt.Errorf("decode: unexpected trailing content")
	}
	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(c *Config) {
	if c.API.Listen == "" {
		c.API.Listen = DefaultListen
	}
	if c.API.BodyLimitBytes == 0 {
		c.API.BodyLimitBytes = DefaultBodyLimitBytes
	}
	if c.Database.MaxConnections == 0 {
		c.Database.MaxConnections = DefaultMaxConnections
	}
	if c.Database.ConnectTimeoutSeconds == 0 {
		c.Database.ConnectTimeoutSeconds = DefaultConnectTimeout
	}
	if c.Retention.Payload.Duration == 0 {
		c.Retention.Payload.Duration = defaultPayloadRetention
	}
	if c.Retention.Result.Duration == 0 {
		c.Retention.Result.Duration = defaultResultRetention
	}
	if c.Retention.Metadata.Duration == 0 {
		c.Retention.Metadata.Duration = defaultMetadataRetention
	}
	for name, w := range c.Workloads {
		if w.PriorityClass == "" {
			w.PriorityClass = "normal"
		}
		if w.MaxActiveTasks == 0 {
			w.MaxActiveTasks = DefaultMaxActiveTasks
		}
		if w.MaxAttempts == 0 {
			w.MaxAttempts = defaultMaxAttempts
		}
		if len(w.RetryDelaysSeconds) == 0 {
			w.RetryDelaysSeconds = []int{defaultRetryDelay1, defaultRetryDelay2}
		}
		if w.MaxOutputTokens == 0 {
			w.MaxOutputTokens = DefaultMaxOutputTokens
		}
		c.Workloads[name] = w
	}
	for name, b := range c.Backends {
		if b.Type == AdapterOpenAICompatible && b.ResponseHeaderTimeoutSeconds == 0 {
			b.ResponseHeaderTimeoutSeconds = DefaultResponseHeaderTimeoutSeconds
			c.Backends[name] = b
		}
	}
}

// rejectDuplicateFields is intentionally independent from struct decoding:
// encoding/json otherwise accepts duplicate object keys, which would make a
// signed/ reviewed configuration different from the one actually applied.
func rejectDuplicateFields(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(dec); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("decode: unexpected trailing content")
	}
	return nil
}

func walkJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch d := tok.(type) {
	case json.Delim:
		switch d {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, ok := seen[name]; ok {
					return fmt.Errorf("duplicate field %q", name)
				}
				seen[name] = struct{}{}
				if err := walkJSONValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token() // object close
			return err
		case '[':
			for dec.More() {
				if err := walkJSONValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token() // array close
			return err
		}
	}
	return nil
}

// Default retention values.
const (
	defaultPayloadRetention  = 1 * time.Hour
	defaultResultRetention   = 7 * 24 * time.Hour
	defaultMetadataRetention = 30 * 24 * time.Hour
	defaultMaxAttempts       = 3
	defaultRetryDelay1       = 1
	defaultRetryDelay2       = 5
)

// jsonPath renders a dotted field path from a json.UnmarshalTypeError.
func jsonPath(field string) string {
	if field == "" {
		return "(root)"
	}
	return field
}

// jsonUnmarshalString unmarshals b into a string field.
func jsonUnmarshalString(b []byte, s *string) error {
	if len(b) == 0 || b[0] != '"' {
		return fmt.Errorf("expected a JSON string, got %s", truncateBytes(b, 32))
	}
	return json.Unmarshal(b, s)
}

// jsonMarshalString marshals s as a JSON string.
func jsonMarshalString(s string) ([]byte, error) { return json.Marshal(s) }

func truncateBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// FilePath returns a clean absolute path for error messages.
func FilePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
