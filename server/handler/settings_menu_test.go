package handler

import "testing"

func TestValidateMenuLayout(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty allowed", "", false},
		{"valid object", `{"version":1,"nodes":[]}`, false},
		{"valid with nodes", `{"version":1,"nodes":[{"kind":"item","key":"about","enabled":true}]}`, false},
		{"invalid json", `{not json`, true},
		{"null", `null`, true},
		{"array not object", `[]`, true},
		{"number", `123`, true},
		{"too large", string(make([]byte, maxMenuLayoutBytes+1)), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateMenuLayout(c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateMenuLayout err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}
