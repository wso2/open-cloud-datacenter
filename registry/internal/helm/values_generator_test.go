package helm

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func testValuesInput(namespace string) ValuesInput {
	plan, _ := PlanFor("starter")
	return ValuesInput{
		Namespace:     namespace,
		BaseDomain:    "example.com",
		StorageClass:  "longhorn",
		IngressClass:  "nginx",
		CertIssuer:    "internal-ca",
		Plan:          plan,
		SecretName:    "harbor-harbor-admin",
		DBPass:        "dbpass1234567890",
		EncryptionKey: "enckey1234567890",
	}
}

func TestGenerateValues_ProducesValidYAML(t *testing.T) {
	out, err := GenerateValues(testValuesInput("acme"))
	if err != nil {
		t.Fatalf("GenerateValues() error = %v", err)
	}
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("rendered values are not valid YAML: %v\n---\n%s", err, out)
	}
}

// A value containing YAML-significant characters must never break the document
// structure or silently mutate a different field. Namespace is exercised here
// because it reaches the most rendered fields; real namespaces are DNS labels,
// so this asserts the template is safe on its own rather than by borrowing
// that guarantee.
func TestGenerateValues_YAMLSpecialCharsDoNotBreakDocument(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
	}{
		{"colon and space", "my: ns"},
		{"embedded newline", "my\nns"},
		{"double quote", `my"ns`},
		{"backslash", `my\ns`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := GenerateValues(testValuesInput(tt.namespace))
			if err != nil {
				t.Fatalf("GenerateValues() error = %v", err)
			}
			var parsed map[string]interface{}
			if err := yaml.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("Namespace %q broke the rendered YAML: %v\n---\n%s", tt.namespace, err, out)
			}
			expose, _ := parsed["expose"].(map[string]interface{})
			tlsSec, _ := expose["tls"].(map[string]interface{})
			secretBlock, _ := tlsSec["secret"].(map[string]interface{})
			gotSecretName, _ := secretBlock["secretName"].(string)
			wantSecretName := tt.namespace + "-harbor-tls"
			if gotSecretName != wantSecretName {
				t.Errorf("secretName round-tripped as %q, want %q — Namespace was not preserved literally", gotSecretName, wantSecretName)
			}
		})
	}
}
