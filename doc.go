// Package mbox reads the mbox files of the archive.org Usenet backups, one
// message at a time: the header lines in file order, and a reader for the body.
// It is the parser behind github.com/go-while/mbox2nntp, and a fork of
// github.com/sam-falvo/mbox.  Writing mbox files is not supported.
//
// It is not a general-purpose mbox reader.  A line separates two messages only
// if it has one of a few known shapes, each taken from a collection that uses
// it; README.md lists them, and they live in sepPatterns in mbox.go.  The classic
// "From user@example.com Thu Dec  8 12:55:04 2016" line is not one of them:
// Usenet articles quote "From " lines in their bodies unescaped, and a generic
// rule would cut those articles in two.  NearMisses shows the body lines that
// looked like a separator but were not taken as one; check it on one file of
// each new collection.
//
// Headers are returned as found, apart from two repairs listed in README.md.
// Bodies are returned line by line with their original line endings; nothing
// is decoded, Content-Length is ignored, and ">From " is not un-escaped.
//
// Using mbox involves creating an io.Reader for the mbox file, then submitting
// that to CreateMboxStream.  Then call ReadMessage for each message, and read
// its body to io.EOF before calling ReadMessage again.  The stream is
// sequential; no means of seeking exists.
//
// The import path and the package name differ:
//
//	import mbox "github.com/go-while/go-lib-mbox2nntp"
package mbox
