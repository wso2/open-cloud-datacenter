package dns

import "testing"

// TestBuildHostname covers the DNS-label sanitization rules the reconciler
// uses to turn a tenant-supplied database name into a per-VPC CoreDNS host
// record. The output drives what customers type into their connection strings,
// so getting this wrong is a UX bug as well as a DNS bug.
func TestBuildHostname(t *testing.T) {
	cases := []struct {
		name    string
		dbName  string
		want    string
	}{
		// Happy path: ASCII-clean name passes through (just lowercased + suffixed).
		{name: "simple", dbName: "orders", want: "orders.db.dc.internal"},
		{name: "preserves-hyphen", dbName: "my-orders-db", want: "my-orders-db.db.dc.internal"},
		{name: "preserves-digit", dbName: "shop-2024", want: "shop-2024.db.dc.internal"},

		// Casing: DNS labels must be lowercase.
		{name: "uppercase-lowered", dbName: "Orders", want: "orders.db.dc.internal"},
		{name: "mixed-case-lowered", dbName: "MyOrdersDB", want: "myordersdb.db.dc.internal"},

		// Invalid characters collapse into a single hyphen.
		{name: "space-becomes-hyphen", dbName: "my orders db", want: "my-orders-db.db.dc.internal"},
		{name: "punctuation-collapsed", dbName: "weird!@#$%name", want: "weird-name.db.dc.internal"},
		{name: "underscore-becomes-hyphen", dbName: "my_orders_db", want: "my-orders-db.db.dc.internal"},
		{name: "leading-trailing-whitespace-trimmed", dbName: "  orders  ", want: "orders.db.dc.internal"},

		// Hyphens at the edges of the label are illegal in DNS — must be trimmed.
		{name: "leading-hyphen-trimmed", dbName: "-orders", want: "orders.db.dc.internal"},
		{name: "trailing-hyphen-trimmed", dbName: "orders-", want: "orders.db.dc.internal"},
		{name: "both-edges-trimmed", dbName: "---orders---", want: "orders.db.dc.internal"},

		// Pathological cases that sanitize to nothing — empty result, the
		// reconciler will refuse to register and surface the issue.
		{name: "all-invalid-empty", dbName: "---", want: ""},
		{name: "only-punctuation-empty", dbName: "!@#$%", want: ""},
		{name: "empty-input-empty", dbName: "", want: ""},

		// 63-char label cap (RFC 1123). A 70-char name truncates; trailing
		// hyphens introduced by truncation are also stripped.
		{
			name:   "over-63-chars-truncated",
			dbName: "a234567890123456789012345678901234567890123456789012345678901234567890",
			want:   "a23456789012345678901234567890123456789012345678901234567890123.db.dc.internal",
		},
		{
			name:   "truncation-trailing-hyphen-stripped",
			dbName: "a23456789012345678901234567890123456789012345678901234567890123-extra",
			want:   "a23456789012345678901234567890123456789012345678901234567890123.db.dc.internal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildHostname(tc.dbName)
			if got != tc.want {
				t.Errorf("BuildHostname(%q):\n  got:  %q\n  want: %q", tc.dbName, got, tc.want)
			}
		})
	}
}
