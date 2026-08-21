package cmd

import "strings"

// docSection returns the lines of the named top-level section, from its heading up to the next
// top-level heading. '### ' subheadings do not terminate it: HasPrefix(line, "## ") is false for a
// line starting "###".
func docSection(doc, heading string) ([]string, bool) {
	var section []string
	found := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") {
			if found {
				break
			}
			found = strings.TrimSpace(line) == heading
			if !found {
				continue
			}
		}
		if found {
			section = append(section, line)
		}
	}
	return section, found
}

// fencedBlocksContaining returns the body of every fenced block in lines opened with the given
// language tag whose body contains needle. It reads the RAW section rather than blankFencedBlocks
// output, which exists to erase exactly this content: the prose scans want fences gone, the example
// checks want nothing else. Scoping to a section rather than the whole document matters —
// a manual has several other ```json blocks, and one of those must not be able to
// satisfy another section's guard.
func fencedBlocksContaining(lines []string, lang, needle string) []string {
	var blocks []string
	var body []string
	// Fence parity is tracked separately from whether this block is one being collected, because a
	// closing fence is bare and is therefore indistinguishable from an opening ```-with-no-language.
	// Matching the language on every fence line instead would open a block on each close and read the
	// prose between two blocks as a block body — which is what an unlabelled needle asks for.
	inFence, capturing := false, false
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inFence {
				if capturing {
					if block := strings.Join(body, "\n"); strings.Contains(block, needle) {
						blocks = append(blocks, block)
					}
					body = nil
				}
				inFence, capturing = false, false
				continue
			}
			inFence = true
			capturing = strings.TrimSpace(line) == "```"+lang
			continue
		}
		if capturing {
			body = append(body, line)
		}
	}
	return blocks
}

// blankFencedBlocks empties fenced code blocks, including their fence lines, keeping every
// other line at its original index so a violation can be reported where the author will find
// it. Fences are matched unindented, which is also what the phase's acceptance-criterion awk
// does — an indented fence is scanned as prose by both, so the two can never disagree.
func blankFencedBlocks(lines []string) []string {
	out := make([]string, len(lines))
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out[i] = line
		}
	}
	return out
}
