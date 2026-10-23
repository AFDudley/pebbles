package main

import (
	"flag"
	"testing"

	"pebbles/internal/pebbles"
)

// TestPayloadFlagAccumulatesEveryOccurrence is the load-bearing one.
//
// The obvious way to write this flag is to copy optionalString.Set, which
// OVERWRITES. That yields a flag which silently keeps only the LAST k=v: the
// command still exits 0 and still writes a comment, so nothing surfaces the
// loss — a typed provenance record would land with one key instead of four and
// read back as a valid-looking partial. That is the fail-silent write class, so
// it gets a named test rather than a code comment.
func TestPayloadFlagAccumulatesEveryOccurrence(t *testing.T) {
	payload := payloadFlag{}
	for _, pair := range []string{"a=1", "b=2", "c=3"} {
		if err := payload.Set(pair); err != nil {
			t.Fatalf("Set(%q): %v", pair, err)
		}
	}
	if len(payload) != 3 {
		t.Fatalf("expected all 3 occurrences retained, got %d: %v", len(payload), payload)
	}
	for key, want := range map[string]string{"a": "1", "b": "2", "c": "3"} {
		if payload[key] != want {
			t.Errorf("payload[%q] = %q, want %q", key, payload[key], want)
		}
	}
}

// TestPayloadFlagSplitsOnFirstEqualsOnly: provenance values carry "=" (base64
// padding, query fragments, timestamps with offsets). Splitting on every "="
// would truncate them silently.
func TestPayloadFlagSplitsOnFirstEqualsOnly(t *testing.T) {
	payload := payloadFlag{}
	if err := payload.Set("exophial.b64=YWJjZA==xyz"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := payload["exophial.b64"]; got != "YWJjZA==xyz" {
		t.Errorf("value truncated at an inner '=': got %q", got)
	}
}

// TestPayloadFlagRejectsMalformedPairs: every one of these must fail LOUD at the
// CLI boundary. A half-formed record written and read back later as a partial is
// worse than no record, because absence is what the consumer treats as "no
// capture" and a partial is not distinguishable from a real one.
func TestPayloadFlagRejectsMalformedPairs(t *testing.T) {
	for _, tc := range []struct{ name, pair string }{
		{"no equals", "nokv"},
		{"empty key", "=v"},
		{"blank key", "  =v"},
		{"reserved body key", "body=hi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (payloadFlag{}).Set(tc.pair); err == nil {
				t.Fatalf("Set(%q) accepted a malformed pair", tc.pair)
			}
		})
	}
}

// TestPayloadFlagRejectsDuplicateKey: last-wins would silently discard the
// earlier value, which is the same fail-silent shape as the overwrite bug above.
func TestPayloadFlagRejectsDuplicateKey(t *testing.T) {
	payload := payloadFlag{}
	if err := payload.Set("k=1"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := payload.Set("k=2"); err == nil {
		t.Fatal("duplicate key accepted; the first value would be silently lost")
	}
	if payload["k"] != "1" {
		t.Errorf("rejected duplicate must not mutate the map, got %q", payload["k"])
	}
}

// TestPayloadFlagIsValueTaking pins the classification reorderFlags depends on.
// If a custom Value stopped being derived as value-taking, `pb comment <id>
// --payload k=v` would hoist the flag without its value and the pair would
// become a positional — the argument-order hazard that silently dropped writes
// before flags-after-positionals was fixed.
func TestPayloadFlagIsValueTaking(t *testing.T) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.Var(payloadFlag{}, "payload", "repeatable typed payload key")
	got := valueTakingFlags(fs)
	for _, spelling := range []string{"-payload", "--payload"} {
		if !got[spelling] {
			t.Errorf("%s must be classified value-taking", spelling)
		}
	}
}

// TestNewCommentEventWithoutPayloadIsUnchanged: the historical wire shape must be
// byte-identical for an ordinary comment, so adding the parameter cannot perturb
// the 8230 comment events already on a real bus.
func TestNewCommentEventWithoutPayloadIsUnchanged(t *testing.T) {
	event := pebbles.NewCommentEvent("pb-1", "a note", "2026-01-01T00:00:00Z", nil)
	if len(event.Payload) != 1 || event.Payload["body"] != "a note" {
		t.Fatalf("nil payload must yield exactly {body}, got %v", event.Payload)
	}
}

// TestNewCommentEventBodyWins: "body" is the one key applyComment requires, so a
// typed key must never be able to blank it.
func TestNewCommentEventBodyWins(t *testing.T) {
	event := pebbles.NewCommentEvent(
		"pb-1", "real body", "2026-01-01T00:00:00Z",
		map[string]string{"body": "", "k": "v"},
	)
	if event.Payload["body"] != "real body" {
		t.Errorf("body was overridden by a typed key: %q", event.Payload["body"])
	}
	if event.Payload["k"] != "v" {
		t.Errorf("typed key lost: %v", event.Payload)
	}
}
