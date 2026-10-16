// vim: ts=8 noexpandtab ai

package mbox

import (
    "io"
    //"log"
    //"regexp"
    //"github.com/dlclark/regexp2"

)


// A Message represents a single message in the file.
type Message struct {
    mbox           *MboxStream
    //headers        map[string][]string
    headers        []string
    sendingAddress string
}

// A bodyReader implements an io.Reader, confined to the current message to
// which this instance is bound.
type bodyReader struct {
    msg    *Message
    mbox   *MboxStream
    where  int
    srcErr error
}

// Sender() tells who sent the message.  This corresponds to the e-mail address
// following the From marker that identifies the start of the current message.
// Note that this field may not necessarily match any "From" header as indicated
// in the set of headers returned by Headers().
func (m *Message) Sender() string {
    return m.sendingAddress
}

// The Headers method returns the header lines of a message in file order,
// without their line endings.  A continuation line is an entry of its own,
// keeping its leading whitespace; trailing whitespace is removed.  Given
//
//     Subject: Code Review
//     X-Trace-Token: DEADBEEF-FEED-FACE-0123-45-67-89-AB-CD-EF;
//             user_id=sfalvo
//
// Headers returns
//
//     []string{
//             "Subject: Code Review",
//             "X-Trace-Token: DEADBEEF-FEED-FACE-0123-45-67-89-AB-CD-EF;",
//             "        user_id=sfalvo",
//     }
//
// Upstream returned a map from header name to values, which lost the order of
// the headers; this fork keeps the lines.
//func (m *Message) Headers() map[string][]string {
func (m *Message) Headers() []string {
    return m.headers
}

// BodyReader() provides an io.Reader compatible object that will read the body
// of the message.  It will return io.EOF if you attempt to read beyond the end
// of the message.
func (m *Message) BodyReader() io.Reader {
    br := &bodyReader{
        msg:  m,
        mbox: m.mbox,
    }

    return br
}

func (r *bodyReader) Read(bs []byte) (n int, err error) {
    if r.srcErr != nil {
        return 0, r.srcErr
    }


    // couldBeSep (mbox.go) is a byte-prefix gate in front of validid; it cannot
    // hide a match, and it keeps the regex -- and the string conversion that
    // feeds it -- off every ordinary body line.  It also replaces the old
    // len(prefetch) > 6 guard, which was left over from a prefetch[0:5] slice
    // that no longer exists: no shape validid accepts is shorter than 10 bytes,
    // so that bound never did anything.
    //
    // Only test at the start of a line.  A line longer than bs comes back over
    // several Read calls with r.where > 0; prefetch has not changed, so the
    // verdict cannot either, and re-testing would count a near-miss twice.
    if r.where == 0 && couldBeSep(r.mbox.prefetch) {
        if retbool, err := validid.MatchString(string(r.mbox.prefetch)); retbool == true && err == nil {
            //log.Printf("message.go Read() validid.MatchString str='%s'", string(r.mbox.prefetch))
            return 0, io.EOF
        } else if err == nil {
            // Passed the gate, failed the regex: this line stays in the body.
            r.mbox.recordNearMiss()
        }
    }


    /*
    if (len(r.mbox.prefetch) > 5) && (string(r.mbox.prefetch[0:5]) == "From ") {
        return 0, io.EOF
    }
    */

    n = copy(bs, r.mbox.prefetch[r.where:])
    r.where = r.where + n
    if r.where >= len(r.mbox.prefetch) {
        r.where = 0
        r.srcErr = r.mbox.nextLine()
    }
    return
}
