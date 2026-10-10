package domain

import (
	"strings"
	"testing"
)

func TestCanonicalJSONUsesStableRFC8785Representation(t *testing.T) {
	got, err := CanonicalizeJSON([]byte(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":2,"b":1}` {
		t.Fatalf("canonical JSON = %s, want {\"a\":2,\"b\":1}", got)
	}

	digest, err := DigestCanonicalJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	const want = "d3626ac30a87e6f7a6428233b3c68299976865fa5508e4267c5415c76af7a772"
	if digest != want {
		t.Fatalf("digest = %q, want %q", digest, want)
	}
}

func TestCanonicalJSONPreservesUnicodeAndUsesUTF16PropertyOrder(t *testing.T) {
	got, err := CanonicalizeJSON([]byte("{\"\ufffd\":1,\"\U00010000\":2,\"text\":\"<>&\u2028\"}"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"text\":\"<>&\u2028\",\"\U00010000\":2,\"\ufffd\":1}"
	if string(got) != want {
		t.Fatalf("canonical JSON = %q, want %q", got, want)
	}
}

func TestCanonicalJSONRejectsInvalidUTF8InTypedValues(t *testing.T) {
	_, err := CanonicalJSON(map[string]string{
		"value": string([]byte{0xff}),
	})
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("CanonicalJSON error = %v, want invalid UTF-8 rejection", err)
	}
}

func TestCanonicalJSONRejectsAmbiguousOrUnsupportedNumbers(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "duplicate property", raw: `{"a":1,"a":2}`, want: "duplicate object property"},
		{name: "fraction", raw: `{"a":1.5}`, want: "integer"},
		{name: "exponent", raw: `{"a":1e3}`, want: "integer"},
		{name: "unsafe integer", raw: `{"a":9007199254740992}`, want: "safe integer"},
		{name: "lone high surrogate", raw: `{"a":"\ud800"}`, want: "surrogate"},
		{name: "lone low surrogate", raw: `{"a":"\udc00"}`, want: "surrogate"},
		{name: "trailing value", raw: `{"a":1}{}`, want: "trailing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CanonicalizeJSON([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CanonicalizeJSON error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func FuzzCanonicalJSONIsIdempotent(f *testing.F) {
	f.Add([]byte(`{"b":[3,2,1],"a":{"x":"value"}}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`["\u0000",true,false,-42]`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		first, err := CanonicalizeJSON(raw)
		if err != nil {
			return
		}
		second, err := CanonicalizeJSON(first)
		if err != nil {
			t.Fatalf("canonical JSON could not be canonicalized again: %v", err)
		}
		if string(first) != string(second) {
			t.Fatalf("canonicalization is not idempotent:\nfirst=%q\nsecond=%q", first, second)
		}
	})
}
