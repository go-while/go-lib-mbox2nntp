// vim: ts=8 ai noexpandtab

package mbox

import (
    "bufio"
    "bytes"
    "fmt"
    "io"
    "strings"
    "regexp"
)

var (
    //invalidid = regexp2.MustCompile(`(\%|\;|\,)`, 0)
    //validid = regexp2.MustCompile(`^From\ `, 0)
    //validid = regexp2.MustCompile(`^From\ (MAILER-DAEMON\ |news@gmane\.org\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\ |[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]|-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]|nobody\ [A-Z][a-z][a-z]\s+[0-9]|nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\ |nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\s+[0-9]{1,2}\:[0-9]{1,2}\:[0-9]{1,2}\ [0-9]{4})`, 0)
    //validid = regexp2.MustCompile(`^From\ (news@gmane\.org\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\ |[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]|-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]|nobody\ [A-Z][a-z][a-z]\s+[0-9]|nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\ |nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\s+[0-9]{1,2}\:[0-9]{1,2}\:[0-9]{1,2}\ [0-9]{4})`, 0)
    // The outer ^ is redundant, since every pattern has its own, but Go only
    // sees the whole regex as anchored with it: without it a line that does
    // not match is tried at every offset, 12 us instead of 0.5 us.
    validid = regexp.MustCompile(`^(?:` + strings.Join(sepPatterns, "|") + `)`)
)

// ws is the whitespace class in sepPatterns.  validid was compiled with
// github.com/dlclark/regexp2 until 2026, whose \s is unicode.IsSpace; Go's \s
// is only [\t\n\f\r ].  \v, U+0085 and \p{Z} add what Go's lacks, so no line
// changed verdict.  TestWSIsUnicodeIsSpace holds it to that.
const ws = `[\s\v\x{85}\p{Z}]`

// sepPatterns are the message-separator shapes validid accepts, one per
// alternative.  Each must stay anchored to "^From " or "^#! rnews " (see
// couldBeSep).  None is anchored at the end: "From " followed by eight digits
// accepts any longer number too.
//
// A shape missing here merges every message that uses it into the one before;
// a shape that also matches ordinary text splits a body there.  So add one
// only for a collection that needs it, and run NearMisses over that collection
// first.
var sepPatterns = []string{
    `^#\!\ rnews\ [0-9]+`,                                    // rnews batch files
    `^From\ MAILER-DAEMON\ `,                                 // bounces
    `^From\ news@gmane\.org\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\ `, // gmane: "From news@gmane.org Thu Dec  8 ..."
    `^From\ [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]`,         // usenet-clari: "From 191826460637372077"
    `^From\ -[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]`,        // usenet-clari: "From -191826460637372077"
    `^From\ nobody\ [A-Z][a-z][a-z]` + ws + `+[0-9]`,          // "From nobody Thu  8 Dec 2016"
    `^From\ nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\ `,      // gna-clari: "From nobody Thu Dec  8 12:55:04 2016"
    `^From\ nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]` + ws + `+[0-9]{1,2}\:[0-9]{1,2}\:[0-9]{1,2}\ [0-9]{4}`, // "From nobody Thu Dec\t12:55:04 2016": no day; differs from the one above only when the gap is not one space
}

// Every alternative in validid is anchored to "^From " or "^#! rnews ", so a
// line starting with neither cannot possibly match.  couldBeSep checks that in
// a couple of byte comparisons, which lets the regex be skipped on the ~99.99%
// of body lines that are not separators.
//
// This runs once per body line across the whole archive, so it is the hottest
// path in the importer.  go test -bench Validid, with Go's regexp (2026):
// 15 ns/op gated on an ordinary body line, 0.9 us on a near-miss such as
// "From Table 1", 1.3 us on an actual separator.  With regexp2 it was
// 1.1 us on a non-matching line and 1.5 us on a separator.
//
// TestValididPrefixGate asserts the "cannot hide a match" property this relies
// on; if you add an alternative to validid that is not anchored to one of these
// two prefixes, that test fails and this gate must be widened to match.
var (
    sepFrom  = []byte("From ")
    sepRnews = []byte("#! rnews ")
)

func couldBeSep(p []byte) bool {
    return bytes.HasPrefix(p, sepFrom) || bytes.HasPrefix(p, sepRnews)
}

// MboxStream objects represent sequential streams of e-mail messages that
// exist in the MBOX format.
type MboxStream struct {
    prefetch       []byte
    prefetchLength int
    currentLine    int
    r              *bufio.Reader

    nearMisses     int
    nearMissSample []NearMiss
}

// A NearMiss is a body line that passed couldBeSep -- it starts with "From " or
// "#! rnews " -- but that validid then rejected, so it was kept as body text.
//
// Each one is either ordinary prose that happens to start with "From ", or a
// real message separator in a shape validid does not know, in which case the
// following article has been merged into the current one.  The count cannot
// tell those apart; the samples let a human do it in seconds.
type NearMiss struct {
    Line int    // 1-based line number in the input
    Text string // the line, trailing CR/LF removed, cut to nearMissMaxText bytes
}

const (
    nearMissMaxSamples = 5
    nearMissMaxText    = 200
)

// NearMisses reports how many body lines on this stream looked like a message
// separator but did not match validid, together with the first few of them.
//
// Point this at one file from each new archive collection before importing it:
// a non-zero count with separator-shaped samples means validid needs another
// branch, and the import would otherwise silently merge articles.
func (m *MboxStream) NearMisses() (count int, samples []NearMiss) {
    out := make([]NearMiss, len(m.nearMissSample))
    copy(out, m.nearMissSample)
    return m.nearMisses, out
}

func (m *MboxStream) recordNearMiss() {
    m.nearMisses++
    if len(m.nearMissSample) >= nearMissMaxSamples {
        return
    }
    text := strings.TrimRight(string(m.prefetch), "\r\n")
    if len(text) > nearMissMaxText {
        text = text[:nearMissMaxText]
    }
    m.nearMissSample = append(m.nearMissSample, NearMiss{Line: m.currentLine, Text: text})
}

// The ReadMessage method parses the input for another complete message.  A
// message consists of a From header, at least one header, followed by a
// collection of lines of text corresponding to the body of the message.
//
// If no From marker exists, we either don't have an MBOX file, a corrupted
// MBOX file, or we're at the end of the input stream.  Giving the benefit of
// the doubt, this package returns io.EOF for an error in any of these
// situations.
//
// After reading a message, you must read the body of the message prior to
// reading the next.  Otherwise, a framing error will cause the reader to
// return io.EOF prematurely.  See the SkippingTheBody example for a simple
// example showing how to do this simply.
//
// A message whose headers cannot be parsed is reported as an error, and the
// stream is moved on to the next separator line, so the caller may skip the
// error and call ReadMessage again to carry on with the next message.
func (m *MboxStream) ReadMessage() (msg *Message, err error) {
    var h []string
    msg = &Message{
        mbox:    m,
        //headers: make(map[string][]string, 0),
        headers: h,
    }

    //log.Printf("DEBUG ReadMessage: parseFrom")
    msg.sendingAddress, err = m.parseFrom()
    if err != nil {
        //log.Printf("ReadMessage: parseFrom err=%v", err)
        msg = nil
        return
    }

    //log.Printf("DEBUG ReadMessage: parseHeaders")
    msg.headers, err = m.parseHeaders()
    if err != nil {
        // the line number stays first: callers compare and parse it
        err = fmt.Errorf("%w (message %q)", err, msg.sendingAddress)
        msg = nil
        m.resync()
        return
    }

    //log.Printf("DEBUG ReadMessage: parseBlankline")
    err = m.parseBlankLine()
    if err != nil {
        msg = nil
        m.resync()
        return
    }

    return
}

// errorf provides an error object whose string also includes the line-number.
// TODO(sfalvo): This prevents the user from testing for specific error responses.
// Find a better way of exposing the line on which an error occurs.
func (m *MboxStream) errorf(format string, args ...interface{}) error {
    s := fmt.Sprintf(format, args...)
    return fmt.Errorf("%d:%s", m.currentLine, s)
}

// parseBlankLine will succeed only if the current line of the mbox file is a
// blank line.  Blank lines are required by the MBOX format conventions to separate
// MIME headers from message content.
func (m *MboxStream) parseBlankLine() error {
    // DEBUG
    //log.Printf("parseBlankLine m.prefetch len=%d", len(m.prefetch))

    //if len(m.prefetch) > 1 || m.prefetch[0] != '\n' {
    if len(m.prefetch) > 0 && !isBlankLine(m.prefetch) {
        return m.errorf("Blank line expected")
    }
    return m.nextLine()
}

// parseFrom will succeed only if the current line of the mbox file is a properly
// formed "From " separator.  It will extract the sending e-mail address from this
// line.  If this line doesn't exist, it yields an error instead.
func (m *MboxStream) parseFrom() (who string, err error) {
    who, err = extractSendingAddress(m)
    if err == nil {
        err = m.nextLine()
        if err == nil {
            //log.Printf("\nDEBUG\nmbox FROM who='%s'", who)
        }
    }
    return
}

func extractSendingAddress(m *MboxStream) (who string, err error) {
    //testStr := string(m.prefetch[0:5])
    //if testStr != "From " {
    if !couldBeSep(m.prefetch) {
        return "", io.EOF
    }
    if !validid.Match(m.prefetch) {
        return "", io.EOF
    }
    if m.prefetchLength < 6 {
        return "", m.errorf("Sender address expected")
    }
    who = strings.TrimSpace(string(m.prefetch[5:]))
    if who == "" {
        return "", m.errorf("Sender address cannot be whitespace")
    }
    return
}

/*
func extractSendingAddress_mod(m *MboxStream) (string, error) {
    who := ""
    // 2020/09/26 19:30:47 error in string(m.prefetch)='From 23% sovereignists in 1970, to 40% in 1980, and now 49,6 % !!! And, a poll
    // 2020/09/26 19:40:07 error in string(m.prefetch)='From 0.6, several minor bugs were fixed, and the feature set was extended a
    //validID := regexp.MustCompile(`^From\ ([0-9a-zA-Z\s\:]+|(-)?[0-9]+|nobody\ Tue)`)

    //invalidID := regexp2.MustCompile(`(\%|\;|\,)`, 0)
    if retbool, err := invalidid.MatchString(string(m.prefetch)); retbool == true && err == nil {
        return "", nil
    }
    //validID := regexp2.MustCompile(`^From\ ([0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]+\n$|-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]+\n$|nobody\ [A-Z][a-z][a-z]\s+[0-9]|nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\s+[0-9]|nobody\ [A-Z][a-z][a-z]\ [A-Z][a-z][a-z]\s+[0-9]{1,2}\:[0-9]{1,2}\:[0-9]{1,2}\ [0-9]{4})`, 0)
    //validID := regexp2.MustCompile(`^From\ )`, 0)
    if retbool, err := validid.MatchString(string(m.prefetch)); retbool == true && err == nil {
        who = string(m.prefetch)
        return who, nil
        //return
    } else {
        if !isspace(m.prefetch[0]) {
            log.Printf("error in extractSendingAddress string(m.prefetch)='%s'", string(m.prefetch))
        }
        return "", nil
    }

    if string(m.prefetch[0:5]) != "From " {
        return "", io.EOF
    }

    if m.prefetchLength < 6 {
        return "", m.errorf("Sender address expected")
    }
    who = strings.TrimSpace(string(m.prefetch[5:]))
    if who == "" {
        return "", m.errorf("Sender address cannot be whitespace")
    }
    return who, nil
}
*/

// parseHeaders will read in the headers from the mbox file.  It builds a
// mapping from string to an array of strings.  Each header key corresponds to
// one or more strings as received in the mbox file.  For greatest fidelity,
// leading whitespace on continued lines is preserved.
//func (m *MboxStream) parseHeaders() (hs map[string][]string, err error) {
func (m *MboxStream) parseHeaders() ([]string, error) {
    //hs = make(map[string][]string, 0)
    var head []string
    for {
        //key, values, err := m.parseHeader()
        lines, err := m.parseHeader()
        // debug
        //log.Printf("parseHeaders: key=%s values='%s' err='%v'", key, values, err)
        if err != nil {
            if isBlankLine(m.prefetch) {
                break
            }
            //errstr := fmt.Sprintf("%v", err)
            //if strings.Contains(errstr, "Colon") {
            //    continue
            //}
            return nil, err
            //continue
        }
        /* fixme
        akey := strings.ToLower(key)
        if akey == "message-id" {

        }
        */
        //hs[key] = values
        for _, line := range lines {
            //line = strings.TrimSuffix(line, "\r\n")
            //line = strings.Replace(line,"\r","",-1)
            // strip the line ending, CRLF as well as LF: a surviving '\r'
            // goes out as "\r\r\n" when the article is sent over NNTP.
            line = strings.TrimRight(line, "\r\n")
            head = append(head, line)
        }
        //if m.prefetch[0] == '\n' {
        if len(m.prefetch) == 0 || isBlankLine(m.prefetch) {
            break
        }
    }
    return head, nil
}

// parseHeader will read in a single header from the mbox file.
// Header attributes start with a "key: value" syntax; however, continued
// lines thereafter just start with some flavor of whitespace.
//func (m *MboxStream) parseHeader() (key string, values []string, err error) {
func (m *MboxStream) parseHeader() ([]string, error) {
    // Headers consist of a key, a colon, and a value.  The key must not be an empty string.
    // Therefore, the smallest possible header is K:, which takes up two characters.
    if m.prefetchLength < 2 {
        //return "", nil, m.errorf("Header attribute expected")
        return nil, m.errorf("parseHeader Header attribute expected | m.prefetch='%s'", m.prefetch)
    }

    if isspace(m.prefetch[0]) {
        //return "", nil, m.errorf("Unexpected continuation of a header somehow missed")
        return nil, m.errorf("parseHeader Unexpected continuation of a header somehow missed")
    }
    line := string(m.prefetch)
    if strings.HasPrefix(line, ": ") {
        line = "X-Unknown" + line
    }
    if strings.HasPrefix(line, "x-no-archive; yes") {
        line = "x-no-archive: yes"
    }
    k := strings.Index(line, ":")
    if k < 1 {
        //return "", nil, m.errorf("Colon not found in expected 'key: value' syntax | m.prefetch='%s'", m.prefetch)
        return nil, m.errorf("parseHeader Colon not found in expected 'key: value' syntax | m.prefetch='%s' line='%s'", m.prefetch, line)
    }
    var lines []string
    //key := string(m.prefetch[0:k])
    //values := []string{strings.TrimSpace(string(m.prefetch[k+1:]))}
    err := m.nextLine()
    if err != nil {
        return nil, err
    }
    //line = strings.Replace(line,"\r","",-1)

    //line = strings.Replace(line,"  "," ",-1)

    /*
    if line[len(line)-2] == '\r' && line[len(line)-1] == '\n' {
        line = strings.Replace(line,"\r\n","",-1)
    }
    if line[len(line)-1] == '\n' {
        line = strings.Replace(line,"\n","",-1)
    }
    if line[len(line)-1] == '\r' {
        line = strings.Replace(line,"\r","",-1)
    }
    */
    lines = append(lines, line)
    for {
        // Continuation lines consist of at least one whitespace and at least one regular character.
        // isspace counts '\r' as whitespace, so a CRLF blank line would
        // otherwise be taken as a continuation and the body read as headers.
        if (m.prefetchLength < 2) || (!isspace(m.prefetch[0])) || isBlankLine(m.prefetch) {
            //log.Printf("break read header continuation key='%s' values='%s'", key, values)
            break
        }
        continuation := strings.TrimRight(string(m.prefetch), " \r\n\t\b\v")
        continuation = strings.Replace(continuation,"\r\n","",-1)
        //values = append(values, continuation)
        lines = append(lines, continuation)
        err = m.nextLine()
        if err != nil {
            //return "", nil, err
            return nil, err
        }
        //log.Printf("read header continuation key='%s' values='%s'", key, values)
    }

    return lines, err
}

func (m *MboxStream) parseHeaderOLD() (key string, values []string, err error) {
    // Headers consist of a key, a colon, and a value.  The key must not be an empty string.
    // Therefore, the smallest possible header is K:, which takes up two characters.
    if m.prefetchLength < 2 {
        return "", nil, m.errorf("Header attribute expected")
    }

    if isspace(m.prefetch[0]) {
        return "", nil, m.errorf("Unexpected continuation of a header somehow missed")
    }

    k := strings.Index(string(m.prefetch), ":")
    if k < 1 {
        return "", nil, m.errorf("Colon not found in expected 'key: value' syntax | m.prefetch='%s'", m.prefetch)
    }

    key = string(m.prefetch[0:k])
    values = []string{strings.TrimSpace(string(m.prefetch[k+1:]))}
    err = m.nextLine()
    if err != nil {
        return "", nil, err
    }

    for {
        // Continuation lines consist of at least one whitespace and at least one regular character.
        if (m.prefetchLength < 2) || (!isspace(m.prefetch[0])) {
            break
        }
        continuation := strings.TrimRight(string(m.prefetch), " \r\n\t\b\v")
        values = append(values, continuation)
        err = m.nextLine()
        if err != nil {
            return "", nil, err
        }
    }

    return
}

func isspace(b byte) bool {
    return b < 33
}

// isBlankLine reports whether p is the empty line that ends a header block,
// with either an LF or a CRLF line ending.
func isBlankLine(p []byte) bool {
    return (len(p) == 1 && p[0] == '\n') || (len(p) == 2 && p[0] == '\r' && p[1] == '\n')
}

// resync advances to the next line validid accepts, or to the end of input.
// ReadMessage calls it after a malformed message: without it the stream is
// left inside that message, the next ReadMessage finds no separator, and
// returns io.EOF, silently ending the file there.
func (m *MboxStream) resync() {
    for {
        if couldBeSep(m.prefetch) {
            if validid.Match(m.prefetch) {
                return
            }
        }
        if m.nextLine() != nil {
            return
        }
    }
}

// CreateMboxStream decorates an io.Reader instance with an mbox parser.
// It returns io.EOF if the input is empty (with a nil stream), or if its first
// line is not a separator validid accepts.  Observe, however, that
// CreateMboxStream() succeeding does not imply that it actually is a correctly
// formatted mbox file.
func CreateMboxStream(s io.Reader) (m *MboxStream, err error) {
    m = &MboxStream{
        prefetch: make([]byte, 64*1024),
        r:        bufio.NewReader(s),
    }

    err = m.nextLine()
    if err != nil {
        m = nil
        return
    }

    _, err = extractSendingAddress(m)
    return
}

// nextLine retrieves the next logical line from the mbox file.  The caller
// should be concerned with one of three cases:
//
// - A successful read yields no error.
// - Attempting to read past the end of the input stream yields io.EOF.
// - All other errors are reported as necessary.
func (m *MboxStream) nextLine() error {
    sliceb, err := m.r.ReadBytes('\n')
    //line, err := m.r.ReadSlice('\n')
    if err == io.EOF && len(sliceb) > 0 {
        // The last line of a file that does not end in a newline. It used
        // to be dropped along with the error; terminate it like the others.
        // The next call gets io.EOF with nothing read.
        sliceb, err = append(sliceb, '\n'), nil
    }
    if err != nil {
        return err
    }
    line := string(sliceb)
    /*
    if len(line) >= 1 {
        switch(line[len(line)-1]) { // switch last char in line
            case '\n':
                line = strings.TrimRight(line, "\n")
            case '\r':
                line = strings.TrimRight(line, "\r")
        } // end switch
    }*/
    // DEBUG
    //log.Printf("MboxStream nextLine() line='%s' i=%d", line, m.currentLine)


    // If slice is longer than the original prefetch, resize.
    if len(line) > cap(m.prefetch) {
        m.prefetch = make([]byte, len(line))
    }
    m.prefetch = m.prefetch[0:len(line)]
    copy(m.prefetch, line)
    m.prefetchLength = len(m.prefetch)
    m.currentLine++
    return nil
}
