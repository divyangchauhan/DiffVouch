package tokens

import "testing"

func TestKnownAndUnknownModelCounts(t *testing.T) {
	for _, model := range []string{"gpt-5", "future-model", "claude-model", ""} {
		counter, err := New(model)
		if err != nil {
			t.Fatal(err)
		}
		if counter.Estimated != (model != "gpt-5") {
			t.Fatalf("model %q has incorrect estimate label", model)
		}
		for text, want := range map[string]int{"": 0, "hello world": 2, "Hello, world!": 4} {
			got, err := counter.Count(text)
			if err != nil || got != want {
				t.Fatalf("%q: count %d, want %d, err %v", text, got, want, err)
			}
		}
	}
}
