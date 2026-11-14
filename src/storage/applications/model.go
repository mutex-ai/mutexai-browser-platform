// Package applications owns the global app registry and its normalized host claims.
package applications

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	ErrInvalid          = errors.New("invalid application definition")
	ErrNotFound         = errors.New("application not found")
	ErrRequestConflict  = errors.New("Idempotency-Key was used for a different application request")
	ErrSlugConflict     = errors.New("application slug is already registered")
	ErrHostConflict     = errors.New("host pattern is already registered to another application")
	ErrRevisionConflict = errors.New("application has changed; read it again before editing")
	ErrDependency       = errors.New("application dependencies must identify existing applications other than itself")
	errIDCollision      = errors.New("application ID collision")
)

//go:embed definition.schema.json
var definitionSchema []byte

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(definitionSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	const location = "https://mutex.ai/schemas/application-definition/v1.json"
	if err := c.AddResource(location, doc); err != nil {
		return nil, err
	}
	return c.Compile(location)
})

var appIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ValidID accepts the existing catalog's permanent 12-character hexadecimal IDs.
func ValidID(id string) bool { return appIDPattern.MatchString(id) }

// Definition is an immutable, validated input. Server-owned fields and hosts
// are stored separately; hash covers the complete normalized creation input.
type Definition struct {
	body         json.RawMessage
	hosts        []string
	dependencies []string
	hash         [32]byte
}

// Record preserves the catalog's app document while exposing UTC record times.
// ID and Revision are used for routing and ETags, and are rendered inside App.
type Record struct {
	ID           string          `json:"-"`
	Revision     int64           `json:"-"`
	App          json.RawMessage `json:"app"`
	CreatedTSUTC time.Time       `json:"created_ts_utc"`
	UpdatedTSUTC time.Time       `json:"updated_ts_utc"`
}

// ParseDefinition normalizes before schema validation and request hashing.
// Explicit empty/unknown values are retained instead of round-tripping through
// a partial model whose omitempty fields could change the catalog's meaning.
func ParseDefinition(raw []byte) (Definition, error) {
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return Definition{}, fmt.Errorf("%w: expected at most 64 KiB of UTF-8 JSON", ErrInvalid)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return Definition{}, fmt.Errorf("%w: expected one JSON object", ErrInvalid)
	}
	doc, ok := trimStrings(value).(map[string]any)
	if !ok || doc == nil {
		return Definition{}, fmt.Errorf("%w: expected one JSON object", ErrInvalid)
	}
	if slug, ok := doc["slug"].(string); ok {
		doc["slug"] = strings.ToLower(slug)
	}
	if kind, ok := doc["kind"].(string); ok {
		doc["kind"] = strings.ToLower(kind)
	}
	if _, ok := doc["meta"]; !ok {
		doc["meta"] = map[string]any{"schema_version": json.Number("3")}
	}
	profile, _ := doc["profile"].(map[string]any)
	urls, _ := profile["urls"].(map[string]any)
	hosts, err := normalizeHosts(urls, "hosts", true, true)
	if err != nil {
		return Definition{}, err
	}
	if _, err := normalizeHosts(urls, "homepage", false, false); err != nil {
		return Definition{}, err
	}
	var dependencies []string
	if raw, ok := profile["depends_on"].([]any); ok {
		for _, value := range raw {
			id, ok := value.(string)
			if !ok {
				return Definition{}, fmt.Errorf("%w: invalid dependency ID", ErrInvalid)
			}
			id = strings.ToLower(id)
			if !ValidID(id) {
				return Definition{}, fmt.Errorf("%w: invalid dependency ID", ErrInvalid)
			}
			dependencies = append(dependencies, id)
		}
		slices.Sort(dependencies)
		dependencies = slices.Compact(dependencies)
		profile["depends_on"] = stringValues(dependencies)
	}
	if capabilities, ok := doc["capabilities"].([]any); ok {
		for _, value := range capabilities {
			capability, _ := value.(map[string]any)
			if host, ok := capability["host"].(string); ok && host != "" {
				host, err = normalizeHost(host, true, false)
				if err != nil {
					return Definition{}, fmt.Errorf("%w: invalid capability host", ErrInvalid)
				}
				capability["host"] = host
			}
			if path, ok := capability["path"].(string); ok && path != "" && !strings.HasPrefix(path, "/") {
				return Definition{}, fmt.Errorf("%w: capability paths must start with /", ErrInvalid)
			}
		}
	}
	validator, err := compiledSchema()
	if err != nil {
		return Definition{}, fmt.Errorf("compile application schema: %w", err)
	}
	if err := validator.Validate(doc); err != nil {
		return Definition{}, fmt.Errorf("%w: definition must match catalog schema v3; omit IDs, revisions and write timestamps", ErrInvalid)
	}
	// JSON Schema accepts 3.0 as an integer, while CP's Go models expect an
	// integer token. These are the schema's only numeric definition fields.
	doc["meta"].(map[string]any)["schema_version"] = 3
	if risk, ok := doc["risk"].(map[string]any); ok {
		if score, ok := risk["score"].(json.Number); ok {
			value, err := score.Float64()
			if err != nil {
				return Definition{}, fmt.Errorf("%w: invalid risk score", ErrInvalid)
			}
			risk["score"] = int(value)
		}
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return Definition{}, fmt.Errorf("marshal application definition: %w", err)
	}
	hash := sha256.Sum256(canonical)
	delete(urls, "hosts") // host claims have one authoritative representation in PostgreSQL.
	body, err := json.Marshal(doc)
	if err != nil {
		return Definition{}, fmt.Errorf("marshal application definition: %w", err)
	}
	return Definition{body: body, hosts: hosts, dependencies: dependencies, hash: hash}, nil
}

func trimStrings(value any) any {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for key, item := range v {
			v[key] = trimStrings(item)
		}
	case []any:
		for i, item := range v {
			v[i] = trimStrings(item)
		}
	}
	return value
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
