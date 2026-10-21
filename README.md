# go-lib-mbox2nntp

A Go package (`package mbox`) that reads the mbox files in the archive.org Usenet
backups one message at a time. Each message comes back as its header lines, in order, and
a reader for its body.

It is the parser behind [mbox2nntp](https://github.com/go-while/mbox2nntp), which feeds
these archives to an NNTP server. It is a fork of
[github.com/sam-falvo/mbox](https://github.com/sam-falvo/mbox). In upstream, headers are a
map, which loses their order and repeats; here they are kept as the lines of the file.

**This is not a general-purpose mbox reader.** A line counts as a message separator only if
it has one of the [shapes listed below](#separator-shapes), each taken from an archive
collection that uses it. The classic `From user@example.com Thu Dec  8 12:55:04 2016` line
is *not* one of them. Usenet articles quote `From ` lines in their bodies unescaped, and a
generic rule cuts those articles in two. To read a collection with another separator,
[add its shape](#adding-a-separator-shape).

## Install

```
go get github.com/go-while/go-lib-mbox2nntp
```

The import path and the package name differ, so give the import a name:

```go
import mbox "github.com/go-while/go-lib-mbox2nntp"
```

It has no dependencies outside the Go standard library.

## Example

This program prints the Message-ID and body size of each article in an mbox file:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	mbox "github.com/go-while/go-lib-mbox2nntp"
)

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	mr, err := mbox.CreateMboxStream(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		// io.EOF: the file is empty, or does not start with a known separator.
		log.Fatalf("not an mbox this package can read: %v", err)
	}

	buf := make([]byte, 64*1024)
	var prevErr error
	for {
		msg, err := mr.ReadMessage()
		if err == io.EOF {
			break // the end of the input
		}
		if err != nil {
			// One malformed message, already skipped. The same error twice
			// in a row means the stream is not moving: give up.
			if prevErr != nil && err.Error() == prevErr.Error() {
				log.Fatalf("stuck: %v", err)
			}
			prevErr = err
			log.Printf("skipped a message: %v", err)
			continue
		}
		prevErr = nil

		msgid := ""
		for _, h := range msg.Headers() {
			if strings.HasPrefix(strings.ToLower(h), "message-id:") {
				msgid = strings.TrimSpace(h[len("message-id:"):])
			}
		}

		// Read the body to io.EOF, even when it is not wanted: the next
		// ReadMessage starts where the body ends.
		size := 0
		body := msg.BodyReader()
		for {
			n, err := body.Read(buf)
			if err != nil {
				break
			}
			size += n
		}
		fmt.Printf("%s %d\n", msgid, size)
	}

	n, samples := mr.NearMisses()
	log.Printf("near-misses: %d", n)
	for _, s := range samples {
		log.Printf("  line %d: %q", s.Line, s.Text)
	}
}
```

To get the body as lines, join the pieces of a line until one ends in `\n`, since a line
longer than the buffer arrives over several `Read` calls. `readBody` in
[mbox2nntp.go](https://github.com/go-while/mbox2nntp/blob/main/mbox2nntp.go) does this.

## API

| | |
|---|---|
| `CreateMboxStream(r io.Reader) (*MboxStream, error)` | Wraps `r`. Returns `io.EOF` if `r` is empty or its first line is not a known separator. It reads line by line through its own `bufio.Reader`. |
| `(*MboxStream) ReadMessage() (*Message, error)` | Parses the next separator and header block. |
| `(*Message) Headers() []string` | The header lines, in file order. |
| `(*Message) BodyReader() io.Reader` | The body, up to the next separator. |
| `(*Message) Sender() string` | The separator line without its first five bytes (`"From "`), trimmed, such as `nobody Thu Dec  8 12:55:04 2016` or `-191826460637372077`. It is meaningless for `#! rnews` lines. |
| `(*MboxStream) NearMisses() (int, []NearMiss)` | Body lines that looked like a separator but were not taken as one. See [below](#near-misses). |

The [godoc examples](mbox_test.go) (`ExampleMboxStream` and others) also run as tests.

### Reading a stream

- **Read every body to `io.EOF` before the next `ReadMessage`.** The stream has one read
  position. If a body is left unread, `ReadMessage` finds body text where it expects a
  separator, and returns `io.EOF` as if the file had ended.
- **`io.EOF` from `ReadMessage` means the end of the input.** As described above, it also
  comes back when the current line is not a separator.
- **Any other error from `ReadMessage` costs one message.** The headers could not be
  parsed, for example a line with no colon. The stream has already moved to the next
  separator, so call `ReadMessage` again. Error text starts with the line number, as in
  `3:parseHeader Colon not found ...`. Stop if the same error comes back twice in a row.
- **`ErrLineTooLong` ends the stream.** A line longer than `MaxLineLength` (64 MiB,
  line ending included) is not read. `ReadMessage` and the body reader return the error,
  wrapped with the line number, from then on; test it with `errors.Is`. Set
  `MaxLineLength` before reading to change the limit.
- **`io.EOF` does not prove the whole file was read.** A stream that loses its place,
  for example after an unread body or a read error, ends early with the same `io.EOF`. To
  tell, wrap the reader you pass to `CreateMboxStream` and check that it returned `io.EOF`
  itself. mbox2nntp does this in `unreadBytes`.
- **A stream is sequential, with no seeking.** Use one goroutine per stream. Separate
  streams are independent.

### Headers

`Headers()` returns the header block as lines without their line endings, `\n` or `\r\n`.

- A continuation line is an entry of its own, keeping its leading whitespace and
  losing its trailing whitespace. `X-Long: a` followed by `\tb` gives
  `["X-Long: a", "\tb"]`.
- Two broken shapes are repaired: a line starting `: value` becomes `X-Unknown: value`,
  and `x-no-archive; yes` becomes `x-no-archive: yes`.
- The block ends at the first blank line, LF or CRLF. Nothing else is changed or
  decoded.
- A separator also ends the block. The message then has headers and an empty body, and
  the separator starts the next message. A separator in place of the first header line is
  an error that costs this message.

### Bodies

- Each `Read` returns at most one line, with its line ending as in the file (`\n` or
  `\r\n`). A line longer than the buffer comes back over several `Read` calls. Lines up to
  `MaxLineLength` are read whole.
- The last line of a file that does not end in a newline gets `\n` added.
- The blank line in front of the next separator is part of this body.
- `>From ` lines are not un-escaped. The archive.org collections measured so far are not
  mbox-escaped, so a `>From ` line there is part of the article as posted.
- Nothing is decoded: no MIME, no transfer encodings, no yEnc.

### Logging

The package does not log. Everything it knows about a problem is in the error it
returns: a header error carries the line number first, then the offending line and the
separator of the message it cost, as in
`3:parseHeader Colon not found ... (message "nobody Thu Dec  8 12:55:04 2016")`.
Log those errors with your own prefix. Until October 2026 it also wrote to the standard
`log` package: a line per message, one at the end of every stream, and each error a second
time.

## Separator shapes

These are the patterns in `sepPatterns` in [mbox.go](mbox.go):

| shape | example | where it is used |
|---|---|---|
| `#! rnews N` | `#! rnews 1234` | rnews batch files |
| `From MAILER-DAEMON ` | `From MAILER-DAEMON Thu Dec  8 12:55:04 2016` | bounces |
| `From news@gmane.org Ddd Mmm ` | `From news@gmane.org Thu Dec  8 12:55:04 2016` | gmane |
| `From ` and 8 or more digits | `From 191826460637372077` | usenet-clari |
| `From -` and 8 or more digits | `From -191826460637372077` | usenet-clari |
| `From nobody Ddd Mmm ...` | `From nobody Thu Dec  8 12:55:04 2016` | gna-clari, the `usenet-*` items |
| `From nobody Ddd D...` | `From nobody Fri 23 Oct 2020 05:16:14 AM CEST` | the `FULL-USENET-BACKUP-*` items |

Only the start of a line is tested, so a few body lines are mistaken for separators and
cut their article short. Two examples are `From MAILER-DAEMON I got this bounce:` and
`From nobody Thu 3 people replied`. `TestValididSeparatorShapes` lists these, and the shapes
that are not accepted.

### Near-misses

A near-miss is a body line that starts with `From ` or `#! rnews ` but did not match a
shape, so it stayed in the body. `NearMisses()` returns the count for the stream so far,
and the first five lines with their line numbers.

- In ordinary text near-misses are normal, such as `From Table 1 we see`.
- If the samples look like separators (`From someone@example.com Thu Dec  8 ...`), the file
  uses a shape this package does not know. Every message after such a separator was merged
  into the one before it.

Check one file from each new collection before importing it. `mbox2nntp -dry-run FILE`
prints the count and the samples.

### Adding a separator shape

1. Find the near-misses in a few files of the collection and look at the samples.
2. Add the narrowest pattern that matches those separators to `sepPatterns`. Name the
   collection in a comment.
   - Anchor it to `^From\ ` or `^#\!\ rnews\ `. Lines with neither prefix are rejected
     before the regex runs. `TestSepPatternsMatchOldValidid` fails on a pattern with
     another anchor.
   - The wider the pattern, the more body lines it will take for separators, in every
     collection.
3. Add the separator and some body lines it must not match to
   `TestValididSeparatorShapes`.
4. `TestSepPatternsMatchOldValidid` compares the list with the regex as it was before it
   was split into patterns. A new shape changes the answer for the lines it now accepts.
   Update that test in the same commit.
5. Run `go test ./...`. Then run the collection again: its near-miss count should drop to
   ordinary text.

## Tests

```
go test ./...
```

The tests need no files or network. They cover:

- the separator shapes, and the prefix check in front of the regex;
- headers with CRLF line endings;
- skipping a malformed message and carrying on;
- near-misses;
- a final line without a newline;
- the godoc examples.

`go test -bench Validid` measures the separator check on body lines and on separators.

## License

See [LICENSE](LICENSE).
