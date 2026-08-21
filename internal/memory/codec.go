package memory

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// fence is the frontmatter delimiter. Obsidian requires "---" to render Properties, and
	// TOML frontmatter ("+++") is not an option: Obsidian does not render it at all.
	fence = "---"
	// dateLayout is date-only because that is what an operator hand-edits in the note, and it is
	// what the index summarises a note by. It is a distinct layout from created's RFC3339 on
	// purpose — the two answer questions at different resolutions — and both are named here so
	// nobody invents a third.
	dateLayout = "2006-01-02"
	// maxSlugLen bounds a note id, mirroring maxSessionIDLen in internal/statusline/daily.go.
	maxSlugLen = 128
)

// Parse decodes a note. It NEVER returns an error: a file that fails the dialect comes back
// with Malformed set and its text preserved in Body, because the caller may be a session-start
// hook where a returned error would silently darken the whole memory channel (ADR-007). The
// degradation is per field — one unreadable line does not cost the fields around it.
//
// The body begins after the FIRST closing fence and is never re-scanned, so a body that quotes
// a frontmatter block cannot promote its own note.
func Parse(data []byte) Note {
	fm, body, ok := splitFrontmatter(string(data))
	if !ok {
		// Neither half is trustworthy, so nothing is discarded: the whole file becomes the body.
		return Note{Status: StatusActive, Body: string(data), Malformed: true}
	}

	n := Note{Status: StatusActive, Body: body}
	for _, line := range strings.Split(fm, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// A CRLF file's trailing '\r' needs no special handling here: it is whitespace, so the
		// TrimSpace below removes it along with the ordinary padding around a value.
		key, value, found := strings.Cut(line, ":")
		if !found {
			n.Malformed = true
			continue
		}
		n.setField(strings.TrimSpace(key), stripComment(strings.TrimSpace(value)))
	}
	return n
}

// setField assigns one frontmatter pair. An unrecognised key is tolerated on read and dropped
// on the next canonical emit — see Emit for why that is a decision rather than a bug.
func (n *Note) setField(key, value string) {
	switch key {
	case "id":
		n.ID = unquoteScalar(value)
	case "agent":
		n.Agent = unquoteScalar(value)
	case "formula":
		n.Formula = unquoteScalar(value)
	case "run":
		n.Run = unquoteScalar(value)
	case "type":
		n.Type = unquoteScalar(value)
		// The value is KEPT even when it is not one of ours, so the operator can see what they
		// typed; the flag is what stops the note being served against a TTL chosen for a
		// vocabulary it is not in.
		if n.Type != "" && !knownType(n.Type) {
			n.Malformed = true
		}
	case "status":
		// An operator who deleted the status line still wants the note. Reading a missing or
		// blank status as anything but active would silently drop it from every read path.
		if s := unquoteScalar(value); s != "" {
			n.Status = s
			if !knownStatus(s) {
				n.Malformed = true
			}
		}
	case "graduated_to":
		n.GraduatedTo = unquoteScalar(value)
	case "created":
		// Symmetric with expires below on BOTH branches: an operator who blanked the line left no
		// date, which is a missing value, not an unparseable one (reading it as malformed would make
		// their note unmarkable — the mark operations refuse to rewrite what they could not fully
		// read); and a non-empty value is parsed through the same parseExpires tolerance — a bare
		// date (2006-01-02) or full RFC3339 — so an operator who simplifies created to a bare date in
		// Obsidian, the way expires and index.md already render dates, is honored, not silently dropped.
		if unquoteScalar(value) == "" {
			return
		}
		t, err := parseExpires(unquoteScalar(value))
		if err != nil {
			n.Malformed = true
			return
		}
		n.Created = t.UTC()
	case "expires":
		// Empty is the documented "no TTL, review-nagged instead" state, not a parse failure.
		if unquoteScalar(value) == "" {
			return
		}
		t, err := parseExpires(unquoteScalar(value))
		if err != nil {
			n.Malformed = true
			return
		}
		n.Expires = t
	case "evidence":
		if unquoteScalar(value) == "" {
			return
		}
		// A fresh slice per note: json.Unmarshal leaves the destination partially populated on
		// some errors, and a half-filled evidence list is worse than none.
		var ev []string
		if err := json.Unmarshal([]byte(value), &ev); err != nil {
			n.Malformed = true
			return
		}
		n.Evidence = ev
	}
}

// stripComment removes a trailing "# …" annotation from a frontmatter value. The dialect has to
// accept them because the design's own canonical note carries them on half its fields
// (data.md:48-64) and because both YAML and Obsidian read them that way — absorbing one into the
// value instead is not cosmetic: `type: improvement  # nagged` becomes a type nothing recognises,
// and the note class that must never expire quietly acquires a TTL.
//
// Only a '#' at the start of the value or preceded by whitespace opens a comment, so "issue#626"
// keeps its hash. A quoted or flow value is cut only where what remains is still well-formed
// JSON, which is what lets a '#' INSIDE a string ("a #b") stay content.
//
// Emit∘Parse stays closed because of an agreement with isBareSafe, not because of this function
// alone: everything Emit quotes survives the guard above, and isBareSafe refuses to write a
// value beginning with '#' bare precisely because THIS function would read it as a comment.
// Neither half is sufficient on its own — a value beginning with '#' emitted bare parses back
// as empty, silently.
func stripComment(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] != '#' {
			continue
		}
		if i > 0 && value[i-1] != ' ' && value[i-1] != '\t' {
			continue
		}
		head := strings.TrimRight(value[:i], " \t")
		if (value[0] == '"' || value[0] == '[') && !json.Valid([]byte(head)) {
			continue
		}
		return head
	}
	return value
}

func parseExpires(v string) (time.Time, error) {
	if t, err := time.Parse(dateLayout, v); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// Emit renders a note canonically: a fixed key order, one spelling per value, and every field
// taken from the struct. Nothing that was parsed but not modelled survives the round trip, so
// an Obsidian property an operator added is READ tolerantly and DROPPED the next time an agent
// marks the note. That is the cost of a canonical emitter, and it is why the mark operations
// refuse to rewrite a note they could not fully parse.
//
// Emit normalises, so Parse(Emit(n)) equals n up to five things: a body that does not end in a
// newline gets one (these are files); created is written at second resolution; expires is written
// as a DATE, so a TTL stamped at 23:59 lands on that day's midnight and the note expires up to a
// day early — immaterial against a 90-day TTL, and the price of a field an operator can hand-edit;
// an empty Status comes back "active", which is Parse's documented default; and Malformed comes
// back false, because it is a verdict Parse reaches about bytes, not a field Emit can write.
// Emit itself remains a fixpoint of Emit∘Parse, which is the property the mark operations depend
// on: they re-emit a note they parsed and must not drift its shape.
func Emit(n Note) []byte {
	var b strings.Builder
	b.WriteString(fence + "\n")
	writeScalar(&b, "id", n.ID)
	writeScalar(&b, "agent", n.Agent)
	writeScalar(&b, "formula", n.Formula)
	writeScalar(&b, "run", n.Run)
	writeScalar(&b, "type", n.Type)

	b.WriteString("created: ")
	if !n.Created.IsZero() {
		b.WriteString(n.Created.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n")

	b.WriteString("evidence: ")
	if len(n.Evidence) > 0 {
		// Marshalling the array with the same encoder that reads it closes the round trip by
		// construction rather than by agreement between two hand-written spellings.
		if raw, err := json.Marshal(n.Evidence); err == nil {
			b.Write(raw)
		}
	}
	b.WriteString("\n")

	writeScalar(&b, "status", n.Status)
	writeScalar(&b, "graduated_to", n.GraduatedTo)

	b.WriteString("expires: ")
	if !n.Expires.IsZero() {
		b.WriteString(n.Expires.UTC().Format(dateLayout))
	}
	b.WriteString("\n")

	b.WriteString(fence + "\n")
	b.WriteString(n.Body)
	if n.Body != "" && !strings.HasSuffix(n.Body, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// writeScalar emits a value bare when the reader would give it back unchanged and JSON-quoted
// otherwise — "unambiguous" is defined by what Parse does with it, not by what looks tidy, which
// is what keeps emit∘parse closed for every valid-UTF-8 value the CLI will hand it. Empty renders
// as "" rather than as nothing, which is also how the design's canonical note spells an unset
// graduated_to.
func writeScalar(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(": ")
	if isBareSafe(value) {
		b.WriteString(value)
	} else {
		// json.Marshal cannot fail for a string — the errors it reports are for unsupported
		// types — so there is no failure branch to write here. Its one lossy case is invalid
		// UTF-8, which it SUBSTITUTES with U+FFFD rather than refusing; Write rejects such a note
		// so this package never creates one, and the substitution survives only where a hand
		// authored file is re-emitted by a mark, where mangled bytes beat refusing the mark.
		raw, _ := json.Marshal(value)
		b.Write(raw)
	}
	b.WriteString("\n")
}

// isBareSafe reports whether a value can be written without quotes and read back identically.
// The allowlist covers every shape the design's own vocabulary uses — slugs, agent names,
// worktree/session pairs, issue#N, commit:<sha>, doc:<path>, formula:<name>@<hash> — and
// excludes anything that would make the line ambiguous to re-read.
func isBareSafe(v string) bool {
	// Two characters are safe as content but not as the FIRST one, so the leading position gets
	// its own test. '#' is what our own reader treats as a comment opener, so a value written bare
	// with one would be read back as empty — silently, and permanently on the next canonical
	// rewrite. '@' is reserved by YAML itself: a real parser rejects a plain scalar that starts
	// with it, and Obsidian rendering these files as Properties is the one contract this dialect
	// exists to satisfy — an unreadable note is worse than a quoted one. Both are ordinary content
	// anywhere else ("issue#631", "run@2026-08-15"), so the loop below still admits them.
	if v == "" || v[0] == '#' || v[0] == '@' {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':', c == '/', c == '@', c == '#', c == '+', c == '=':
		default:
			return false
		}
	}
	return true
}

// unquoteScalar accepts both forms this codec can emit, and both forms a human might type.
func unquoteScalar(v string) string {
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		var s string
		if err := json.Unmarshal([]byte(v), &s); err == nil {
			return s
		}
	}
	return v
}

// splitFrontmatter returns the frontmatter block and the body. It reports !ok when the header
// is absent or unterminated — the two cases where the file cannot be divided at all.
func splitFrontmatter(s string) (fm, body string, ok bool) {
	rest, ok := cutFenceLine(s)
	if !ok {
		return "", "", false
	}
	for idx := 0; ; {
		line, nl := rest[idx:], -1
		if i := strings.IndexByte(rest[idx:], '\n'); i >= 0 {
			nl = idx + i
			line = rest[idx:nl]
		}
		if strings.TrimRight(line, "\r") == fence {
			if nl < 0 {
				return rest[:idx], "", true
			}
			return rest[:idx], rest[nl+1:], true
		}
		if nl < 0 {
			return "", "", false
		}
		idx = nl + 1
	}
}

func cutFenceLine(s string) (string, bool) {
	if rest, ok := strings.CutPrefix(s, fence+"\n"); ok {
		return rest, true
	}
	return strings.CutPrefix(s, fence+"\r\n")
}

// sanitizeSlug reduces a semi-trusted note id to [A-Za-z0-9_-]{0,maxSlugLen}; the empty result
// signals "no usable id" and the caller falls back to a timestamp-only stem.
//
// Copied in shape from internal/statusline/daily.go:359-368 rather than imported: that helper
// is unexported, and internal/memory importing internal/statusline would invert the dependency
// — the same call internal/transcript/reader.go:16-25 makes when it copies a line reader and
// says so. The charset deliberately excludes '.' and '/', the two characters that turn a
// filename slug into a traversal vector, which is why config.ValidateAgentName (which rejects
// rather than transforms, and so would fail on ordinary agent-authored titles) and
// isValidBranchName (which permits both characters) are not substitutes here.
func sanitizeSlug(id string) string {
	b := make([]byte, 0, len(id))
	for i := 0; i < len(id) && len(b) < maxSlugLen; i++ {
		c := id[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b = append(b, c)
		}
	}
	return string(b)
}
