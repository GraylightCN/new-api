package volcadapter

import (
	"strings"
	"testing"
)

// TestModelListNoDuplicates verifies there are no duplicate model IDs.
func TestModelListNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(ModelList))
	for _, m := range ModelList {
		if seen[m] {
			t.Errorf("duplicate model in ModelList: %q", m)
		}
		seen[m] = true
	}
}

// TestModelListNotEmpty verifies the list is populated.
func TestModelListNotEmpty(t *testing.T) {
	if len(ModelList) == 0 {
		t.Fatal("ModelList must not be empty")
	}
}

// TestAllModelsHaveKnownPrefix verifies every model is either a seedream or
// seedance variant (with or without the doubao- prefix).
func TestAllModelsHaveKnownPrefix(t *testing.T) {
	for _, m := range ModelList {
		lower := strings.ToLower(m)
		if !strings.Contains(lower, "seedream") && !strings.Contains(lower, "seedance") {
			t.Errorf("unexpected model %q: must contain 'seedream' or 'seedance'", m)
		}
	}
}

// TestChannelNameNotEmpty verifies the channel name constant is set.
func TestChannelNameNotEmpty(t *testing.T) {
	if ChannelName == "" {
		t.Fatal("ChannelName must not be empty")
	}
}
