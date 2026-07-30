package helm

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func testValuesInput(tenantID string) ValuesInput {
	plan, _ := PlanFor("starter")
	return ValuesInput{
		TenantID:      tenantID,
		BaseDomain:    "example.com",
		StorageClass:  "longhorn",
		IngressClass:  "nginx",
		CertIssuer:    "internal-ca",
		Plan:          plan,
		SecretName:    "rb-acme-harbor-secrets",
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

// TenantID flows into the rendered YAML unquoted from a plain CRD string
// field with no CEL/regex pattern restricting its characters. A tenant ID
// containing YAML-significant characters must never break the document
// structure or silently mutate a different field.
func TestGenerateValues_TenantIDWithYAMLSpecialCharsDoesNotBreakDocument(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
	}{
		{"colon and space", "my: tenant"},
		{"embedded newline", "my\ntenant"},
		{"double quote", `my"tenant`},
		{"backslash", `my\tenant`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := GenerateValues(testValuesInput(tt.tenantID))
			if err != nil {
				t.Fatalf("GenerateValues() error = %v", err)
			}
			var parsed map[string]interface{}
			if err := yaml.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("TenantID %q broke the rendered YAML: %v\n---\n%s", tt.tenantID, err, out)
			}
			expose, _ := parsed["expose"].(map[string]interface{})
			tlsSec, _ := expose["tls"].(map[string]interface{})
			secretBlock, _ := tlsSec["secret"].(map[string]interface{})
			gotSecretName, _ := secretBlock["secretName"].(string)
			wantSecretName := tt.tenantID + "-harbor-tls"
			if gotSecretName != wantSecretName {
				t.Errorf("secretName round-tripped as %q, want %q — TenantID was not preserved literally", gotSecretName, wantSecretName)
			}
		})
	}
}
