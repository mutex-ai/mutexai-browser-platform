package applications

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const exampleDefinition = `{"slug":"example","kind":"application","profile":{"name":"Example","urls":{"homepage":["example.com"],"hosts":["example.com"]}},"classification":{"category":"productivity","subcategory":"documents","modalities":{"in":[],"out":[]}}}`

func TestDefinitionNormalization(t *testing.T) {
	input := strings.ReplaceAll(exampleDefinition, `"example"`, `" EXAMPLE "`)
	input = strings.ReplaceAll(input, `"application"`, `" APPLICATION "`)
	input = strings.Replace(input, `"hosts":["example.com"]`, `"hosts":[" WWW.Example.COM. ","example.com","*.BÜCHER.de.","contoso.EXAMPLE.com"]`, 1)
	got, err := ParseDefinition([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.hosts, ",") != "*.xn--bcher-kva.de,contoso.example.com,example.com" {
		t.Fatalf("unexpected normalized hosts: %v", got.hosts)
	}
	if strings.Contains(string(got.body), `"hosts"`) {
		t.Fatal("host claims must not also be stored in the definition")
	}
	canonical := strings.Replace(exampleDefinition, `"hosts":["example.com"]`, `"hosts":["contoso.example.com","example.com","*.xn--bcher-kva.de"]`, 1)
	same, err := ParseDefinition([]byte(canonical))
	if err != nil || got.hash != same.hash {
		t.Fatalf("equivalent requests have different creation hashes: %v", err)
	}
	// Preserve explicitly present empty values, rather than dropping them with omitempty.
	withEmpty, err := ParseDefinition([]byte(strings.Replace(exampleDefinition, `"name":"Example"`, `"name":"Example","vendor":""`, 1)))
	if err != nil || !strings.Contains(string(withEmpty.body), `"vendor":""`) {
		t.Fatal("explicit empty value lost")
	}
	// Literal backslash text is valid; only the decoded NUL character is rejected.
	withEscape, err := ParseDefinition([]byte(strings.Replace(exampleDefinition, `"name":"Example"`, `"name":"Example\\u0000"`, 1)))
	if err != nil || !strings.Contains(string(withEscape.body), `Example\\u0000`) {
		t.Fatalf("literal escape text changed or was rejected: %v", err)
	}
}

func TestHostNormalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" WWW.Example.COM. ", "example.com"},
		{"bücher.de", "xn--bcher-kva.de"},
		{"*.BÜCHER.de.", "*.xn--bcher-kva.de"},
		{"*-MY.SharePoint.COM", "*-my.sharepoint.com"},
		{"*.wd*.myworkday.com", "*.wd*.myworkday.com"},
		{"www.www.Example.COM", "example.com"},
		{"www.com", "www.com"},
		{"www.co.uk", "www.co.uk"},
		{"WWW.BÜCHER.DE。", "xn--bcher-kva.de"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeHost(tc.input, true, true)
			if err != nil || got != tc.want {
				t.Fatalf("host = %q, %v; want %q", got, err, tc.want)
			}
			if again, err := normalizeHost(got, true, true); err != nil || again != got {
				t.Fatalf("normalization is not stable: %q -> %q, %v", got, again, err)
			}
		})
	}
	for _, input := range []string{"", "localhost", "https://example.com", "example.com/path", "example.com:443", "user@example.com", "127.0.0.1", "[::1]", ".example.com", "bad..example.com", "-bad.example.com", "bad_.example.com", "example.com..", "example.com\n.evil.com", strings.Repeat("x", 64) + ".com", "*bücher.de"} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeHost(input, true, true); err == nil {
				t.Fatalf("invalid host accepted: %q", input)
			}
		})
	}
}

func FuzzHostNormalization(f *testing.F) {
	for _, host := range []string{"example.com", "WWW.BÜCHER.DE。", "www.www.example.com", "www.co.uk", "*.example.com", "*-my.sharepoint.com", "bad..example.com"} {
		f.Add(host)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		first, err := normalizeHost(raw, true, true)
		if err != nil {
			return
		}
		second, err := normalizeHost(first, true, true)
		if err != nil || first != second {
			t.Fatalf("normalization is not stable: %q -> %q -> %q (%v)", raw, first, second, err)
		}
	})
}

func TestDefinitionValidation(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"null", "null"}, {"array", "[]"}, {"empty", "{}"},
		{"unknown field", strings.Replace(exampleDefinition, `"slug":`, `"unused":true,"slug":`, 1)},
		{"managed ID", strings.Replace(exampleDefinition, `"slug":`, `"id":"abcdef123456","slug":`, 1)},
		{"managed revision", strings.Replace(exampleDefinition, `"classification":`, `"meta":{"schema_version":3,"revision":7},"classification":`, 1)},
		{"managed timestamp", strings.Replace(exampleDefinition, `"classification":`, `"meta":{"schema_version":3,"updated_at":"yesterday"},"classification":`, 1)},
		{"bad category tree", strings.Replace(exampleDefinition, `"documents"`, `"chatbot"`, 1)},
		{"invalid slug", strings.Replace(exampleDefinition, `"slug":"example"`, `"slug":"bad_slug"`, 1)},
		{"empty name", strings.Replace(exampleDefinition, `"name":"Example"`, `"name":"  "`, 1)},
		{"NUL in text", strings.Replace(exampleDefinition, `"name":"Example"`, `"name":"Example\u0000"`, 1)},
		{"invalid host", strings.Replace(exampleDefinition, `"hosts":["example.com"]`, `"hosts":["https://example.com"]`, 1)},
		{"null profile", strings.Replace(exampleDefinition, `"name":"Example"`, `"name":null`, 1)},
		{"bad dependency", strings.Replace(exampleDefinition, `"name":"Example"`, `"depends_on":["arbitrary"],"name":"Example"`, 1)},
		{"oversized", strings.Repeat(" ", 65537)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDefinition([]byte(tc.input)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid input accepted or unsafe validation failed: %v", err)
			}
		})
	}
	// Identity records intentionally have no function category; their hosts must remain available to CP.
	var identity map[string]any
	if err := json.Unmarshal([]byte(exampleDefinition), &identity); err != nil {
		t.Fatal(err)
	}
	identity["kind"] = "identity"
	identity["classification"] = map[string]any{"modalities": map[string]any{"in": []string{}, "out": []string{}}}
	raw, _ := json.Marshal(identity)
	if _, err := ParseDefinition(raw); err != nil {
		t.Fatalf("valid identity definition rejected: %v", err)
	}
}

func TestIntegerDefinitionFieldsAreCanonical(t *testing.T) {
	input := strings.Replace(exampleDefinition, `"classification":`, `"meta":{"schema_version":3.0},"risk":{"score":1e0},"classification":`, 1)
	got, err := ParseDefinition([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	var typed struct {
		Meta struct {
			SchemaVersion int `json:"schema_version"`
		} `json:"meta"`
		Risk struct {
			Score int `json:"score"`
		} `json:"risk"`
	}
	if err := json.Unmarshal(got.body, &typed); err != nil || typed.Meta.SchemaVersion != 3 || typed.Risk.Score != 1 {
		t.Fatalf("stored integers are incompatible with CP's typed model: %s %v", got.body, err)
	}
	canonical := strings.Replace(input, "3.0", "3", 1)
	canonical = strings.Replace(canonical, "1e0", "1", 1)
	same, err := ParseDefinition([]byte(canonical))
	if err != nil || got.hash != same.hash {
		t.Fatal("numeric formatting changed normalized creation identity")
	}
}
