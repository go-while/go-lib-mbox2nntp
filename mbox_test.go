// vim: ts=8 noexpandtab ai

package mbox

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// NOTE ON FIXTURES
//
// Upstream (github.com/sam-falvo/mbox) used "From foo@bar.com" as the message
// separator throughout.  This fork narrowed validid (mbox.go) to the handful of
// separator shapes found in the ClariNet archives, and "From <address>" is not
// among them -- CreateMboxStream rejects it with io.EOF.  The fixtures below
// therefore use "From nobody Thu Dec  8 12:55:04 2016", which is the shape the
// gna-clari archives actually carry.
//
// TestValididSeparatorShapes at the bottom of this file pins down exactly which
// shapes are accepted and which are not, including the ones that are arguably
// wrong in both directions.  Change validid and that test tells you what moved.

const mboxWith1Message = `From nobody Thu Dec  8 12:55:04 2016
Subject: Hello world

Test message
`

const mboxWith3Messages = `From nobody Thu Dec  8 12:55:04 2016
Subject: Hello world

Test message



From nobody Thu Dec  8 12:55:05 2016
From: Foo S. Ball <foo@bar.com>
To: Anyone W. Cares <anyone@bar.com>
Subject: You're all fired!

Haha, just joking.
I wasn't really trying to be a jerk.
It's just that it's April fools, and all.
From nobody Thu Dec  8 12:55:06 2016
From: Foo S. Ball <foo@bar.com>
To: Loraine <amiga@bar.com>
Subject: Stella rules!

Old flames never die out.  They just smolder and smoke until you leave the room.
BTW, thanks for the Boing beach ball.

`

const mboxWithMessageNoHeaders = `From nobody Thu Dec  8 12:55:04 2016

Test message
`

const mboxWithMessageNoAttribute = `From nobody Thu Dec  8 12:55:04 2016
 continuation-line

Test message
`

const mboxWithMessageKeyMissing = `From nobody Thu Dec  8 12:55:04 2016
: value-line

Test message
`

const mboxWithMessageHeaderWithContinuation = `From nobody Thu Dec  8 12:55:04 2016
Subject: Hello
 world

Test message
`

const mboxWithMessage3Headers = `From nobody Thu Dec  8 12:55:04 2016
From: foo@bar.com
To: user1@bar.com
 user2@bar.com
 user3@bar.com
 user4@bar.com
 user5@bar.com
Subject: Hello world

Greetings and hallucinations!
`

/* *** Test Utilities *** */

// in() returns true only if a string (needle) is found in an array of strings
// (haystack).
func in(needle string, haystack []string) (found bool) {
	found = false
	n := strings.TrimSpace(needle)
	for _, straw := range haystack {
		if strings.TrimSpace(straw) == n {
			found = true
		}
	}
	return
}

// headerLines returns the lines belonging to the named header, folded
// continuation lines included.
//
// Headers() returns a flat []string of raw header lines in source order -- that
// is the whole point of the fork's change away from map[string][]string, which
// could not preserve either order or duplicate keys.  Continuation lines are the
// ones starting with whitespace, per RFC 822 folding, and they keep their
// leading whitespace.
func headerLines(hs []string, key string) []string {
	var out []string
	prefix := key + ":"
	for i := 0; i < len(hs); i++ {
		if !strings.HasPrefix(hs[i], prefix) {
			continue
		}
		out = append(out, hs[i])
		for i+1 < len(hs) && len(hs[i+1]) > 0 && isspace(hs[i+1][0]) {
			i++
			out = append(out, hs[i])
		}
		return out
	}
	return nil
}

// headerValue returns the value of the named header's first line, with the
// "Key:" stripped and surrounding whitespace removed.
func headerValue(hs []string, key string) string {
	lines := headerLines(hs, key)
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(lines[0], key+":"))
}

// sameStrings reports whether two string slices are element-wise equal.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

/* *** Setups of various kinds *** */

// withOpenMboxStream sets up a test.  It creates a MboxStream on a given
// string source.  If successful, it invokes the specified test, which then
// performs whatever checks it sees fit.
func withOpenMboxStream(t *testing.T, procname, source string, test func(mr *MboxStream)) {
	stringReader := strings.NewReader(source)
	mr, err := CreateMboxStream(stringReader)
	if err != nil {
		t.Error(procname, ": ", err)
		return
	}
	test(mr)
}

// withReadMessage sets up a test.  It creates a MboxStream on a
// known-good mbox file, then reads the first message in the mbox file.  The
// test then performs whichever checks it likes on the provided message.
func withReadMessage(t *testing.T, procname string, test func(msg *Message)) {
	withOpenMboxStream(t, procname, mboxWith1Message, func(mr *MboxStream) {
		msg, err := mr.ReadMessage()
		if err != nil {
			t.Error(procname, ": ", err)
			return
		}
		test(msg)
	})
}

// expectError() performs a basic sanity check for opening a new MboxStream object.
// This procedure checks for the absence of an error, and fails the test if found.
func expectError(t *testing.T, s string, msg string) {
	stringReader := strings.NewReader(s)
	_, err := CreateMboxStream(stringReader)
	if err == nil {
		t.Error(msg)
	}
}

// expectError() performs a basic sanity check for opening a new MboxStream object.
// This procedure checks for the existence of an error, and fails the test if found.
func expectNoError(t *testing.T, s string, msg string, pmr **MboxStream) {
	var err error

	stringReader := strings.NewReader(s)
	*pmr, err = CreateMboxStream(stringReader)
	if err != nil {
		t.Error(msg, ":", err)
	}
}

/* *** Test Cases *** */

// Given a corrupted mbox file with a missing From header on the first line
// When I try to open the file
// Then I expect an error.
func TestMalformedMboxFile10(t *testing.T) {
	expectError(
		t,
		"\nFrom nobody Thu Dec  8 12:55:04 2016\n",
		"Mbox files must start with \"From \"",
	)
}

// Given a corrupted mbox file with a From header on the first line
//
//	AND no sender address
//
// When I try to open the file
// Then I expect an error.
//
// Note: these three short fixtures carry no trailing newline, so in this fork
// they fail inside nextLine()'s ReadBytes('\n') with io.EOF before the sender
// checks in extractSendingAddress are ever reached.  An error is still an
// error, but it is not the error the test name promises.
func TestMalformedMboxFile20(t *testing.T) {
	expectError(t, "From ", "Mbox files that are too short must produce an error")
}

// Given a corrupted mbox file with a valid size but an improperly spaced From line
// When I try to open the file
// Then I expect an error.
func TestMalformedMboxFile30(t *testing.T) {
	expectError(t, " From ", "Leading whitespace on the From line must produce an error")
}

// Given a corrupted mbox file with a valid size but an otherwise empty From line
//
//	AND I successfully open the file
//
// When I try to read the first message
// Then I expect an error.
func TestMalformedMboxFile40(t *testing.T) {
	expectError(t, "From   \t\t  \t\t", "Sender address cannot be whitespace")
}

// Given a valid mbox file
// When I try to open the file
// Then I expect no error and a valid MboxStream instance.
func TestOkMboxFile10(t *testing.T) {
	var mr *MboxStream

	expectNoError(t, mboxWith1Message, "Mbox file with one valid message should not yield an error.", &mr)

	if mr == nil {
		t.Error("Returned MboxStream is nil for some reason")
	}
}

// Given a valid mbox file
//
//	AND I successfully open the file
//
// When I try to read from the file
// Then I expect no error and a valid message instance.
func TestOkMboxFile20(t *testing.T) {
	withReadMessage(t, "TestOkMboxFile20", func(msg *Message) {
		if msg == nil {
			t.Error("Message instance is nil despite lack of error")
		}
	})
}

// Given a valid mbox file
//
//	AND I successfully open the file
//
// When I read from the file
// Then I expect a message with correct sending address.
func TestOkMboxFile30(t *testing.T) {
	withReadMessage(t, "TestOkMboxFile30", func(msg *Message) {
		if msg.Sender() != "nobody Thu Dec  8 12:55:04 2016" {
			t.Errorf("TestOkMboxFile30: Expected valid sending address, got %q", msg.Sender())
			return
		}
	})
}

// Given a valid mbox file
//
//	AND I successfully open the file
//
// When I read from the file
// Then I expect a message with correct headers.
func TestOkMboxFile40(t *testing.T) {
	withReadMessage(t, "TestOkMboxFile40", func(msg *Message) {
		hs := msg.Headers()
		want := []string{"Subject: Hello world"}
		if !sameStrings(hs, want) {
			t.Errorf("TestOkMboxFile40: headers = %q, want %q", hs, want)
			return
		}
		if headerValue(hs, "Subject") != "Hello world" {
			t.Error("TestOkMboxFile40: Subject isn't Hello World")
			return
		}
	})
}

// Given an mbox file with zero headers
//
//	AND I successfully open the file
//
// When I read from the file
// Then I expect the message to be accepted, with no headers and an intact body.
//
// Upstream treated a header-less message as malformed and errored out.  This
// fork accepts it: parseHeaders breaks out of its loop as soon as the prefetched
// line is blank, returning an empty header set rather than an error.  That is
// what lets the header-less stubs in the gna-clari archives through instead of
// aborting the file, so it is deliberate -- but it is the opposite of what
// upstream's test asserted, and nothing recorded the change until now.
func TestMalformedMboxFile50(t *testing.T) {
	withOpenMboxStream(t, "TestMalformedMboxFile50", mboxWithMessageNoHeaders, func(mr *MboxStream) {
		msg, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestMalformedMboxFile50: header-less message should be accepted: ", err)
			return
		}
		if len(msg.Headers()) != 0 {
			t.Errorf("TestMalformedMboxFile50: headers = %q, want none", msg.Headers())
			return
		}
		br := msg.BodyReader()
		buf := make([]byte, 256)
		body := ""
		for {
			n, e := br.Read(buf)
			body += string(buf[:n])
			if e != nil {
				break
			}
		}
		if body != "Test message\n" {
			t.Errorf("TestMalformedMboxFile50: body = %q, want %q", body, "Test message\n")
		}
	})
}

// Given an invalid mbox file with corrupted headers
//
//	AND I successfully open the file
//
// When I read from the file
// Then I expect an error.
func TestMalformedMboxFile60(t *testing.T) {
	withOpenMboxStream(t, "TestMalformedMboxFile60", mboxWithMessageNoAttribute, func(mr *MboxStream) {
		_, err := mr.ReadMessage()
		if err == nil {
			t.Error("TestMalformedMboxFile60: Error expected for missing 'key: value' syntax")
			return
		}
	})
}

// Given an invalid mbox file with a malformed key/value pair
//
//	AND I successfully open the file
//
// When I read from the file
// Then I expect the line to be salvaged under an X-Unknown key.
//
// Upstream rejected a leading ": value-line" outright.  This fork rewrites it to
// "X-Unknown: value-line" (mbox.go, parseHeader) rather than losing the message,
// which is the right call for archive import but is a deliberate behaviour change.
func TestMalformedMboxFile70(t *testing.T) {
	withOpenMboxStream(t, "TestMalformedMboxFile70", mboxWithMessageKeyMissing, func(mr *MboxStream) {
		msg, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestMalformedMboxFile70: ", err)
			return
		}
		want := []string{"X-Unknown: value-line"}
		if !sameStrings(msg.Headers(), want) {
			t.Errorf("TestMalformedMboxFile70: headers = %q, want %q", msg.Headers(), want)
			return
		}
	})
}

// Given a valid mbox file with a key/value pair with at least one continuation line
// When I read the file
// Then I expect the attribute line and its continuation, in order.
func TestOkMboxFile50(t *testing.T) {
	withOpenMboxStream(t, "TestOkMboxFile50", mboxWithMessageHeaderWithContinuation, func(mr *MboxStream) {
		msg, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestOkMboxFile50: ", err)
			return
		}
		hs := msg.Headers()
		want := []string{"Subject: Hello", " world"}
		if !sameStrings(hs, want) {
			t.Errorf("TestOkMboxFile50: headers = %q, want %q", hs, want)
			return
		}
		s := headerLines(hs, "Subject")
		if len(s) != 2 {
			t.Error("One key/value line and one continuation line should give us two lines total")
			return
		}
		if s[0] != "Subject: Hello" {
			t.Error("String extraction seems to have failed for attribute line")
			return
		}
		if s[1] != " world" {
			t.Error("String extraction seems to have failed for continuation: ")
			return
		}
	})
}

// Given a valid mbox with a message using three headers
// When I read the message
// Then I expect to see all three headers, in the order they appear in the source.
//
// Order is the reason Headers() is a []string and not a map[string][]string:
// a map cannot round-trip header order, and NNTP peers care about it.
func TestOkMboxFile60(t *testing.T) {
	withOpenMboxStream(t, "TestOkMboxFile60", mboxWithMessage3Headers, func(mr *MboxStream) {
		msg, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestOkMboxFile60: ", err)
			return
		}
		hs := msg.Headers()
		want := []string{
			"From: foo@bar.com",
			"To: user1@bar.com",
			" user2@bar.com",
			" user3@bar.com",
			" user4@bar.com",
			" user5@bar.com",
			"Subject: Hello world",
		}
		if !sameStrings(hs, want) {
			t.Errorf("TestOkMboxFile60: headers = %q, want %q", hs, want)
			return
		}
		if len(headerLines(hs, "Subject")) != 1 {
			t.Error("Subject should have one value string")
			return
		}
		if len(headerLines(hs, "To")) != 5 {
			t.Error("Multiple recipients should be listed")
			return
		}
		if len(headerLines(hs, "From")) != 1 {
			t.Error("From: header should have one value")
			return
		}
		ff := headerValue(hs, "From")
		tt := headerLines(hs, "To")
		ss := headerValue(hs, "Subject")
		if ff != "foo@bar.com" {
			t.Error("From: header has wrong value")
			return
		}
		for _, a := range []string{"user1@bar.com", "user2@bar.com", "user3@bar.com", "user4@bar.com", "user5@bar.com"} {
			if !in("To: "+a, tt) && !in(a, tt) {
				t.Error("To header values missing an expected value: ", a)
				return
			}
		}
		if ss != "Hello world" {
			t.Error("Subject heading is wrong")
			return
		}
	})
}

// Given a valid mbox with a message with a body
// When I read the message
// Then I expect to access an io.Reader that lets me read in the body.
func TestOkMboxFile70(t *testing.T) {
	withOpenMboxStream(t, "TestOkMboxFile70", mboxWith1Message, func(mr *MboxStream) {
		msg, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestOkMboxFile70: ", err)
			return
		}
		br := msg.BodyReader()
		bs := make([]byte, 128)
		n, err := br.Read(bs)
		if err != nil {
			t.Error("TestOkMboxFile70: ", err)
			return
		}
		if n < 13 {
			t.Error("Expected 13 characters, got ", n)
			return
		}
		bs = bs[0:n]
		if string(bs) != "Test message\n" {
			t.Error("Expected Test message, but got ", string(bs))
			return
		}
		n, err = br.Read(bs)
		if err != io.EOF {
			t.Error("Expected io.EOF; got ", err, n, string(bs))
		}
	})
}

// Given a valid mbox with three messages
// When I read the messages,
// Then I expect to see each message in turn.
func TestOkMboxFile80(t *testing.T) {
	withOpenMboxStream(t, "TestOkMboxFile80", mboxWith3Messages, func(mr *MboxStream) {
		msg1, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestOkMboxFile80: ", err)
			return
		}
		msg2, err := mr.ReadMessage()
		if err == nil {
			t.Error("Expected error here because we haven't finished reading the body of msg1 yet")
			return
		}
		br := msg1.BodyReader()
		bs := make([]byte, 1000)
		err = nil
		for err == nil {
			bs = bs[:cap(bs)]
			n, err := br.Read(bs)
			bs = bs[:n]

			if err == io.EOF {
				break
			} else if err != nil {
				t.Error("TestOkMboxFile80: ", err)
				return
			}
		}

		msg2, err = mr.ReadMessage()
		if err != nil {
			t.Error("TestOkMboxFile80: ", err)
			return
		}
		br = msg2.BodyReader()
		bs = make([]byte, 1000)
		err = nil
		for err == nil {
			bs = bs[:cap(bs)]
			n, err := br.Read(bs)
			bs = bs[:n]

			if err == io.EOF {
				break
			} else if err != nil {
				t.Error("TestOkMboxFile80: ", err)
				return
			}
		}

		msg3, err := mr.ReadMessage()
		if err != nil {
			t.Error("TestOkMboxFile80: ", err)
			return
		}
		br = msg3.BodyReader()
		bs = make([]byte, 1000)
		err = nil
		for err == nil {
			bs = bs[:cap(bs)]
			n, err := br.Read(bs)
			bs = bs[:n]

			if err == io.EOF {
				break
			} else if err != nil {
				t.Error("TestOkMboxFile80: ", err)
				return
			}
		}

		_, err = mr.ReadMessage()
		if err != io.EOF {
			t.Error("EOF expected after reading all messages; getting ", err)
			return
		}
	})
}

// TestValididSeparatorShapes pins down which "From " lines this fork accepts as
// message separators.  validid (mbox.go) is consulted in two places with
// opposite failure modes -- extractSendingAddress rejects a message when it does
// not match, and bodyReader.Read ends a body when it does -- so a change here
// both drops articles and merges them.  Nothing else in the tree covers it.
//
// Several rows below are wrong, and are recorded as wrong on purpose:
// mailer-daemon in lowercase and the classic "From user@domain" separator are
// rejected, while "From MAILER-DAEMON I got this bounce:" occurring in a body is
// accepted and will truncate the article.  None of these shapes appear in the
// ClariNet archives this fork was built for.  Fix a row by all means; just do it
// deliberately.
func TestValididSeparatorShapes(t *testing.T) {
	cases := []struct {
		line   string
		accept bool
		note   string
	}{
		{"#! rnews 1234", true, "rnews batch header"},
		{"From MAILER-DAEMON ", true, "bounce separator"},
		{"From news@gmane.org Thu Dec  8 12:55:04 2016", true, "gmane"},
		{"From 191826460637372077", true, "usenet-clari, unsigned"},
		{"From -191826460637372077", true, "usenet-clari, signed"},
		{"From nobody Thu Dec  8 12:55:04 2016", true, "gna-clari"},
		{"From nobody Thu  8 Dec 2016", true, "day before month"},

		// False positives: body text that will be mistaken for a separator.
		{"From MAILER-DAEMON I got this bounce:", true, "FALSE POSITIVE, truncates body"},
		{"From nobody Thu 3 people replied", true, "FALSE POSITIVE, truncates body"},

		// Gaps: real separator shapes this fork will not accept.
		{"From foo@bar.com", false, "GAP, upstream's own fixture shape"},
		{"From user@domain.com Thu Dec  8 12:55:04 2016", false, "GAP, classic mbox separator"},
		{"From mailer-daemon Thu Dec  8 12:55:04 2016", false, "GAP, lowercase daemon"},
		{"From ???@??? Thu Dec  8 12:55:04 2016", false, "GAP, Eudora/Pine shape"},
		{"From 1234567", false, "GAP, 7-digit id, needs 8"},

		// Correctly rejected.
		{" From nobody Thu Dec  8 12:55:04 2016", false, "leading whitespace"},
		{"Subject: not a separator", false, "ordinary header"},
		{">From nobody Thu Dec  8 12:55:04 2016", false, "mboxrd-escaped body line"},
		{"", false, "empty line"},
	}

	for _, c := range cases {
		got, err := validid.MatchString(c.line)
		if err != nil {
			t.Errorf("validid.MatchString(%q): unexpected error %v", c.line, err)
			continue
		}
		if got != c.accept {
			t.Errorf("validid.MatchString(%q) = %v, want %v (%s)", c.line, got, c.accept, c.note)
		}
	}
}

// gateCorpus is a mix of real separator shapes and the kinds of body and header
// lines that dominate an archive, used by the gate tests and benchmarks below.
var gateCorpus = []string{
	// Separators validid accepts.
	"#! rnews 1234",
	"From MAILER-DAEMON ",
	"From news@gmane.org Thu Dec  8 12:55:04 2016",
	"From 191826460637372077",
	"From -191826460637372077",
	"From nobody Thu Dec  8 12:55:04 2016",

	// "From "-prefixed lines validid nonetheless rejects: these are what the
	// gate cannot help with, and what keeps it honest.
	"From foo@bar.com",
	"From user@domain.com Thu Dec  8 12:55:04 2016",
	"From mailer-daemon Thu Dec  8 12:55:04 2016",
	"From 1234567",
	"From ",
	"From x",

	// Ordinary traffic: the overwhelming majority of lines, all of which the
	// gate should reject without ever entering the regex engine.
	"",
	"\n",
	"Subject: not a separator",
	"Received: from mail.example.com by relay.example.net",
	" From nobody Thu Dec  8 12:55:04 2016",
	">From nobody Thu Dec  8 12:55:04 2016",
	"Fromage is not a separator either",
	"In article <abc@def>, someone wrote:",
	"> quoted text from a previous article",
	"On Thu, 8 Dec 2016, someone wrote:",
	"-- ",
	"begin 644 attachment.bin",
	"M7VJpbmFyeSBnYXJiYWdlIHRoYXQgbG9va3MgbGlrZSB1dWVuY29kZWQgZGF0YQ",
}

// acceptedShapes is one example of each branch validid accepts.
var acceptedShapes = []string{
	"#! rnews 1234",
	"From MAILER-DAEMON ",
	"From news@gmane.org Thu Dec  8 12:55:04 2016",
	"From 191826460637372077",
	"From -191826460637372077",
	"From nobody Thu Dec  8 12:55:04 2016",
}

// TestValididPrefixGate proves that gating validid behind a cheap byte-prefix
// check cannot change its verdict: every shape validid accepts also starts with
// "From " or "#! rnews ".  This is the precondition for putting that gate in
// front of the regex on the hot path, where it is worth roughly two orders of
// magnitude per line.
//
// If you add an alternative to validid that is not anchored to one of those two
// prefixes, this test fails and couldBeSep must be widened to match.
func TestValididPrefixGate(t *testing.T) {
	for _, line := range gateCorpus {
		matched, err := validid.MatchString(line)
		if err != nil {
			t.Errorf("validid.MatchString(%q): unexpected error %v", line, err)
			continue
		}
		if matched && !couldBeSep([]byte(line)) {
			t.Errorf("gate would hide a match: %q", line)
		}
	}
}

// TestValididGateIsTransparent is the differential test: for every line, the
// gated path and the bare regex must reach the same verdict.  TestValididPrefixGate
// checks the one-way property of the gate itself; this checks the composition
// actually used in extractSendingAddress and bodyReader.Read.
func TestValididGateIsTransparent(t *testing.T) {
	lines := append([]string{}, gateCorpus...)
	// Also feed it every truncation of every accepted shape, which walks the
	// regex through all of its partial-match states.
	for _, full := range acceptedShapes {
		for i := 0; i <= len(full); i++ {
			lines = append(lines, full[:i])
		}
	}

	for _, line := range lines {
		want, err := validid.MatchString(line)
		if err != nil {
			t.Errorf("validid.MatchString(%q): unexpected error %v", line, err)
			continue
		}
		got := false
		if couldBeSep([]byte(line)) {
			got, err = validid.MatchString(line)
			if err != nil {
				t.Errorf("gated validid.MatchString(%q): unexpected error %v", line, err)
				continue
			}
		}
		if got != want {
			t.Errorf("gated verdict for %q = %v, bare regex = %v", line, got, want)
		}
	}
}

// TestValididMinimumMatchLength backs up removing the old len(prefetch) > 6
// guard from bodyReader.Read.  That bound was left over from a prefetch[0:5]
// slice that no longer exists; the shortest string validid can accept is
// "#! rnews 1" at 10 bytes, so nothing the bound excluded was ever a match.
func TestValididMinimumMatchLength(t *testing.T) {
	const shortest = 10
	for _, full := range acceptedShapes {
		for i := 0; i < shortest && i <= len(full); i++ {
			matched, err := validid.MatchString(full[:i])
			if err != nil {
				t.Errorf("validid.MatchString(%q): unexpected error %v", full[:i], err)
				continue
			}
			if matched {
				t.Errorf("validid matched %q (%d bytes); shortest match was assumed to be %d bytes",
					full[:i], i, shortest)
			}
		}
	}
}

// The benchmarks below mirror what the call sites actually do: both
// extractSendingAddress and bodyReader.Read hold the line as []byte in
// m.prefetch and convert it to a string to feed the regex.  So the bare and
// gated variants are given the same []byte input and both pay for that
// conversion where it happens, which makes the pair comparable.
//
// The gate's win is twofold on an ordinary body line: the regex never runs, and
// the string conversion that feeds it never happens either, so the allocation
// goes away with it.

var (
	benchBodyLine  = []byte("> quoted text from a previous article")
	benchSeparator = []byte("From nobody Thu Dec  8 12:55:04 2016")
)

func benchBare(b *testing.B, line []byte) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := validid.MatchString(string(line)); err != nil {
			b.Fatal(err)
		}
	}
}

func benchGated(b *testing.B, line []byte) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if couldBeSep(line) {
			if _, err := validid.MatchString(string(line)); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkValididBareBodyLine(b *testing.B)   { benchBare(b, benchBodyLine) }
func BenchmarkValididGatedBodyLine(b *testing.B)  { benchGated(b, benchBodyLine) }
func BenchmarkValididBareSeparator(b *testing.B)  { benchBare(b, benchSeparator) }
func BenchmarkValididGatedSeparator(b *testing.B) { benchGated(b, benchSeparator) }

/* *** Examples *** */

func ExampleMboxStream() {
	s := `From nobody Thu Dec  8 12:55:04 2016
From: Some User <user@domain.com>
To: anyone <anyone@anywhere.com>
Cc: me <me@home-address.com>
Subject: My first example

Hello world!  This is my first example message.

From nobody Thu Dec  8 12:55:05 2016
From: Another User <another@domain.com>
To: me <me@home-address.com>
Subject: Your second example

Hey!  I see you've finally completed the MBOX reader library!  Here's hoping
you get the data you need from the older files.  Now you can finally write that
Go program to slice and dice data stored in mbox files and send them to
LogStash!

-- 
Another User
>From here until there, I'll be somewhere.
`

	// Open the mbox file (or, equivalently, create a reader on an existing buffer)
	reader := strings.NewReader(s)
	mboxReader, err := CreateMboxStream(reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Can't create mbox reader: %#v\n", err)
		os.Exit(1)
	}

	// Iterate through each message, and process them sequentially.
	msgNumber := 0
	bodyBuf := make([]byte, 1000)
	for err == nil {
		var message *Message

		msgNumber++
		message, err = mboxReader.ReadMessage()
		if err != nil {
			continue
		}

		// Print our message summary, consisting of message number and subject.
		// Headers() hands back the raw header lines in source order, so pick out
		// the one you want by prefix.
		subject := ""
		for _, h := range message.Headers() {
			if strings.HasPrefix(h, "Subject:") {
				subject = strings.TrimSpace(strings.TrimPrefix(h, "Subject:"))
				break
			}
		}
		fmt.Println(msgNumber, subject)

		// Skip over message body
		bodyReader := message.BodyReader()
		for err == nil {
			_, err = bodyReader.Read(bodyBuf)
		}
		if err == io.EOF {
			// We reached the end of the message normally.
			// This is expected behavior.
			err = nil
		}
	}

	if err == io.EOF {
		fmt.Println("Done.")
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to read next message: %#v\n", err)
	}
	// Output:
	// 1 My first example
	// 2 Your second example
	// Done.
}

func ExampleMessage_BodyReader_skippingTheBody() {
	var (
		err error
		msg *Message
	)

	buffer := make([]byte, 1000)
	bodyReader := msg.BodyReader()

	for err == nil {
		_, err = bodyReader.Read(buffer)
	}
}

func ExampleMessage_BodyReader_savingTheBody() {
	var (
		err error
		msg *Message
		n   int
	)

	lines := make([]string, 0)
	bodyReader := msg.BodyReader()
	buffer := make([]byte, 1000)

	for err == nil {
		n, err = bodyReader.Read(buffer)
		if err != nil {
			continue
		}
		lines = append(lines, string(buffer[0:n]))
	}
	if err == io.EOF {
		// lines now contains the collected body of the most recently
		// read message.
	} else {
		// Some error occurred; process accordingly.
	}
}
