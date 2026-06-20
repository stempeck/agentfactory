package session

import (
	"os"
	"strings"
	"testing"
)

// TestShellQuoteDocCommentIsASCIIIdiom guards PR #379 Thread 2: the shellQuote
// doc-comment must describe the POSIX '\'' single-quote-escape idiom in ASCII,
// not a U+201D "smart" double quote. shellQuote is security-relevant (it quotes
// user/operator-controlled values before shell execution), so a corrupted
// doc-comment risks a future maintainer reconciling the correct body to the
// wrong comment. The function body is verified separately by TestShellQuote.
func TestShellQuoteDocCommentIsASCIIIdiom(t *testing.T) {
	data, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatalf("reading session.go: %v", err)
	}
	content := string(data)

	// Negative: no curly "smart" quotes anywhere in the source file.
	for _, r := range []rune{'‘', '’', '“', '”'} {
		if strings.ContainsRune(content, r) {
			t.Errorf("session.go contains smart quote %q (%U); doc-comments must be ASCII", r, r)
		}
	}

	// Positive: the restored doc-comment describes the POSIX '\'' idiom verbatim.
	if !strings.Contains(content, `single quotes with the '\'' idiom.`) {
		t.Error(`shellQuote doc-comment must read "single quotes with the '\'' idiom." (POSIX escape idiom restored)`)
	}
}
