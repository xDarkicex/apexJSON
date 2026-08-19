package apexjson

import "testing"

func TestNumberPreservesJSONToken(t *testing.T) {
	for _, input := range []string{"1", "1.0", "1e3", "-42"} {
		n := Number(input)
		encoded, err := Marshal(n)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", input, err)
		}
		if string(encoded) != input {
			t.Fatalf("Marshal(%q) = %q", input, encoded)
		}

		var decoded Number
		if err := Unmarshal([]byte(input), &decoded); err != nil {
			t.Fatalf("Unmarshal(%q): %v", input, err)
		}
		if decoded.String() != input {
			t.Fatalf("Unmarshal(%q) = %q", input, decoded)
		}
	}
}
