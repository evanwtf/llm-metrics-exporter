// Package tail reads the lines appended to a file since the last read, with
// bounded memory. Log adapters (ds4, MTPLX) build on it.
package tail

import (
	"bytes"
	"io"
	"os"
)

// Reader remembers how far into one file it has read.
type Reader struct {
	path    string
	maxRead int64 // bytes read per call
	maxLine int   // longest line kept

	offset   int64
	info     os.FileInfo // the file the offset belongs to
	partial  []byte      // an incomplete last line
	skipping bool        // inside an over-long line, until its newline
	backlog  int64
}

// New returns a Reader for path that starts at the beginning of the file.
// Starting at the beginning makes an exporter-owned counter mean "since this
// log began", the same as an engine's own counter.
func New(path string, maxRead int64, maxLine int) *Reader {
	return &Reader{path: path, maxRead: maxRead, maxLine: maxLine}
}

// Read calls onLine for each complete line appended since the last call,
// without its line ending. If the file shrank or was replaced since the last
// call, Read calls onReset first and reads the new file from its start.
// The line slice is valid only during the call.
func (r *Reader) Read(onReset func(), onLine func(line []byte)) error {
	f, err := os.Open(r.path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if r.info != nil && (!os.SameFile(r.info, info) || info.Size() < r.offset) {
		r.offset, r.partial, r.skipping = 0, nil, false
		onReset()
	}
	r.info = info
	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, min(r.maxRead, 64<<10))
	var read int64
	for read < r.maxRead {
		n, err := f.Read(buf[:min(int64(len(buf)), r.maxRead-read)])
		if n > 0 {
			read += int64(n)
			r.offset += int64(n)
			r.feed(buf[:n], onLine)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	latest, err := f.Stat()
	if err != nil {
		return err
	}
	r.backlog = max(0, latest.Size()-r.offset)
	return nil
}

// Backlog reports unread bytes after the last successful Read.
func (r *Reader) Backlog() int64 { return r.backlog }

func (r *Reader) feed(chunk []byte, onLine func([]byte)) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			r.keep(chunk)
			return
		}
		if !r.skipping {
			var line []byte
			if len(r.partial) > 0 {
				r.keep(chunk[:i])
				line = r.partial
			} else {
				line = chunk[:i]
			}
			if !r.skipping {
				onLine(bytes.TrimSuffix(line, []byte("\r")))
			}
		}
		r.partial, r.skipping = r.partial[:0], false
		chunk = chunk[i+1:]
	}
}

// keep adds b to the partial line, or starts skipping when the line would
// exceed maxLine.
func (r *Reader) keep(b []byte) {
	if r.skipping {
		return
	}
	if len(r.partial)+len(b) > r.maxLine {
		r.partial, r.skipping = r.partial[:0], true
		return
	}
	r.partial = append(r.partial, b...)
}
