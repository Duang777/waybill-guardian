package storage

import "testing"

func TestParseMode(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  Mode
	}{
		{name: "default", want: ModeJSONL},
		{name: "JSONL", value: " JSONL ", want: ModeJSONL},
		{name: "PostgreSQL", value: " POSTGRES ", want: ModePostgres},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseMode(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ParseMode(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
	if _, err := ParseMode("sqlite"); err == nil {
		t.Fatal("unsupported storage mode was accepted")
	}
}
