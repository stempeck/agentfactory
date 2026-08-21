package telemetry

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestReadBoundedLine characterizes the shared bounded-line reader A-1 extracted from the record
// store (readRecordLine) and the recovery funnel (cmd.readFunnelLine). It is a preservation pin,
// not a behavior-change test: the two callers were byte-identical bar their ceiling, so this locks
// the shared contract at its new home — a future single-copy edit turns THIS test red at the seam
// instead of deep inside one parse path. The ceiling is deliberately small so the boundary is cheap
// and unambiguous.
func TestReadBoundedLine(t *testing.T) {
	const ceiling = 8

	t.Run("a normal line is returned with its newline, not over-long", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("abc\n"))
		line, tooLong, err := ReadBoundedLine(r, ceiling)
		if string(line) != "abc\n" || tooLong || err != nil {
			t.Fatalf("got line=%q tooLong=%v err=%v; want %q,false,nil", line, tooLong, err, "abc\n")
		}
	})

	t.Run("a line exactly at the ceiling is returned intact", func(t *testing.T) {
		// 7 bytes + '\n' == 8 == ceiling.
		r := bufio.NewReader(strings.NewReader("abcdefg\n"))
		line, tooLong, err := ReadBoundedLine(r, ceiling)
		if string(line) != "abcdefg\n" || tooLong || err != nil {
			t.Fatalf("got line=%q tooLong=%v err=%v; want %q,false,nil", line, tooLong, err, "abcdefg\n")
		}
	})

	t.Run("a line one byte over the ceiling is skipped and the read resumes at the next line", func(t *testing.T) {
		// 8 bytes + '\n' == 9 > ceiling; the following line must survive intact.
		r := bufio.NewReader(strings.NewReader("abcdefgh\nnext\n"))

		line, tooLong, err := ReadBoundedLine(r, ceiling)
		if line != nil || !tooLong || err != nil {
			t.Fatalf("over-long line: got line=%q tooLong=%v err=%v; want nil,true,nil", line, tooLong, err)
		}

		line, tooLong, err = ReadBoundedLine(r, ceiling)
		if string(line) != "next\n" || tooLong {
			t.Fatalf("line after over-long: got line=%q tooLong=%v; want %q,false (tail must not be read as a fresh line)", line, tooLong, "next\n")
		}
	})

	t.Run("an over-long line spanning multiple buffer fills is still drained", func(t *testing.T) {
		// A small bufio buffer forces ReadSlice to return ErrBufferFull chunks, exercising the drain
		// loop; the short line after the giant must still be read cleanly.
		big := strings.Repeat("x", 100)
		r := bufio.NewReaderSize(strings.NewReader(big+"\nok\n"), 16)

		line, tooLong, err := ReadBoundedLine(r, ceiling)
		if line != nil || !tooLong || err != nil {
			t.Fatalf("multi-chunk over-long line: got line=%q tooLong=%v err=%v; want nil,true,nil", line, tooLong, err)
		}

		line, tooLong, err = ReadBoundedLine(r, ceiling)
		if string(line) != "ok\n" || tooLong {
			t.Fatalf("line after multi-chunk over-long: got line=%q tooLong=%v; want %q,false", line, tooLong, "ok\n")
		}
	})

	t.Run("a final line without a trailing newline is returned with io.EOF", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader("tail"))
		line, tooLong, err := ReadBoundedLine(r, ceiling)
		if string(line) != "tail" || tooLong || !errors.Is(err, io.EOF) {
			t.Fatalf("got line=%q tooLong=%v err=%v; want %q,false,io.EOF", line, tooLong, err, "tail")
		}
	})
}
