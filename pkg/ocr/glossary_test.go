package ocr

import "testing"

func TestRestoreGlossaryDiacritics(t *testing.T) {
	t.Parallel()

	result := Result{Observations: []Observation{{
		Text: `"mad love" à la Magnun; Bayad entered the maglis as a zarif.`,
	}}}
	count := RestoreGlossaryDiacritics(&result, []string{
		"Maǧnūn", "Bayāḍ", "maǧlis", "ẓarīf",
	})
	if count != 4 {
		t.Fatalf("replacement count: got %d, want 4", count)
	}
	want := `"mad love" à la Maǧnūn; Bayāḍ entered the maǧlis as a ẓarīf.`
	if got := result.Text(); got != want {
		t.Fatalf("restored text: got %q, want %q", got, want)
	}
}

func TestRestoreGlossaryDiacriticsDoesNotGuess(t *testing.T) {
	t.Parallel()

	result := Result{Observations: []Observation{{Text: "narratve majlis"}}}
	count := RestoreGlossaryDiacritics(&result, []string{"narrative", "maǧlis", "māǧlis"})
	if count != 0 {
		t.Fatalf("replacement count: got %d, want 0", count)
	}
	if got := result.Text(); got != "narratve majlis" {
		t.Fatalf("text changed ambiguously: %q", got)
	}
}

func TestRestoreGlossaryDiacriticsPreservesCasePattern(t *testing.T) {
	t.Parallel()

	result := Result{Observations: []Observation{{Text: "HADIT Bayad maglis"}}}
	RestoreGlossaryDiacritics(&result, []string{"Ḥadīṯ", "Bayāḍ", "maǧlis"})
	if got, want := result.Text(), "ḤADĪṮ Bayāḍ maǧlis"; got != want {
		t.Fatalf("restored text: got %q, want %q", got, want)
	}
}
