package ar

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

var (
	magic    = []byte("!<arch>")
	linefeed = []byte{0x60, 0x0A}
)

var (
	ErrMagic    = errors.New("ar: Invalid Magic")
	ErrTooShort = errors.New("ar: write too short")
	ErrTooLong  = errors.New("ar: write too long")
)

type Header struct {
	Name    string
	Uid     int
	Gid     int
	Mode    int
	Length  int
	ModTime time.Time
}

type Writer struct {
	inner io.Writer
	hdr   Header
	err   error
}

func NewWriter(w io.Writer) (*Writer, error) {
	if _, err := w.Write(magic); err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte{linefeed[1]}); err != nil {
		return nil, err
	}
	return &Writer{inner: w}, nil
}

func (w *Writer) WriteHeader(h *Header) error {
	if w.err != nil {
		return w.err
	}
	w.hdr = *h

	buf := new(bytes.Buffer)
	writeHeaderField(buf, path.Base(h.Name)+"/", 16)
	writeHeaderField(buf, strconv.FormatInt(h.ModTime.Unix(), 10), 12)
	writeHeaderField(buf, strconv.FormatInt(int64(h.Uid), 10), 6)
	writeHeaderField(buf, strconv.FormatInt(int64(h.Gid), 10), 6)
	writeHeaderField(buf, strconv.FormatInt(int64(h.Mode), 8), 8)
	writeHeaderField(buf, strconv.FormatInt(int64(h.Length), 10), 10)
	buf.Write(linefeed)

	_, err := io.Copy(w.inner, buf)
	return err
}

func (w *Writer) Write(bs []byte) (int, error) {
	vs := make([]byte, len(bs))
	copy(vs, bs)
	if len(bs)%2 == 1 {
		vs = append(vs, linefeed[1])
	}
	n, err := w.inner.Write(vs)
	if err != nil {
		return n, err
	}
	return len(bs), err
}

func (w *Writer) Close() error {
	return nil
}

type Reader struct {
	inner *bufio.Reader
	hdr   *Header
	err   error
}

func List(file string) ([]*Header, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := NewReader(f)
	if err != nil {
		return nil, err
	}
	var hs []*Header
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		hs = append(hs, h)
		if _, err := io.CopyN(ioutil.Discard, r, int64(h.Length)); err != nil {
			return nil, err
		}
	}
	return hs, nil
}

func NewReader(r io.Reader) (*Reader, error) {
	rs := bufio.NewReader(r)
	bs, err := rs.Peek(len(magic))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(bs, magic) {
		return nil, ErrMagic
	}
	if _, err := rs.Discard(len(bs) + 1); err != nil {
		return nil, err
	}
	return &Reader{inner: rs}, nil
}

func (r *Reader) Next() (*Header, error) {
	var h Header
	if r.err != nil {
		return nil, r.err
	}
	if _, err := r.inner.Peek(16); err != nil {
		return nil, io.EOF
	}
	if err := readFilename(r.inner, &h); err != nil {
		r.err = err
		return nil, err
	}
	if err := readModTime(r.inner, &h); err != nil {
		r.err = err
		return nil, err
	}
	if err := readFileInfos(r.inner, &h); err != nil {
		r.err = err
		return nil, err
	}
	bs := make([]byte, len(linefeed))
	if _, err := r.inner.Read(bs); err != nil || !bytes.Equal(bs, linefeed) {
		return nil, err
	}
	r.hdr = &h
	return r.hdr, r.err
}

func (r *Reader) Read(bs []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	vs := make([]byte, r.hdr.Length)
	n, err := io.ReadFull(r.inner, vs)
	if err != nil {
		r.err = err
	}
	if r.hdr.Length%2 == 1 {
		r.inner.Discard(1)
	}
	return copy(bs, vs[:n]), r.err
}

func readFilename(r io.Reader, h *Header) error {
	bs, err := readHeaderField(r, 16)
	if err != nil {
		return err
	}
	h.Name = string(bs)
	return nil
}

func readModTime(r io.Reader, h *Header) error {
	bs, err := readHeaderField(r, 12)
	if err != nil {
		return err
	}
	i, err := strconv.ParseInt(string(bs), 0, 64)
	if err != nil {
		return err
	}
	h.ModTime = time.Unix(i, 0)
	return nil
}

func readFileInfos(r io.Reader, h *Header) error {
	if bs, err := readHeaderField(r, 6); err != nil {
		return err
	} else {
		i, err := strconv.ParseInt(string(bs), 0, 64)
		if err != nil {
			return err
		}
		h.Uid = int(i)
	}
	if bs, err := readHeaderField(r, 6); err != nil {
		return err
	} else {
		i, err := strconv.ParseInt(string(bs), 0, 64)
		if err != nil {
			return err
		}
		h.Gid = int(i)
	}
	if bs, err := readHeaderField(r, 8); err != nil {
		return err
	} else {
		i, err := strconv.ParseInt(string(bs), 0, 64)
		if err != nil {
			return err
		}
		h.Mode = int(i)
	}
	if bs, err := readHeaderField(r, 10); err != nil {
		return err
	} else {
		i, err := strconv.ParseInt(string(bs), 0, 64)
		if err != nil {
			return err
		}
		h.Length = int(i)
	}
	return nil
}

func readHeaderField(r io.Reader, n int) ([]byte, error) {
	bs := make([]byte, n)
	if _, err := io.ReadFull(r, bs); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(bs), nil
}

func writeHeaderField(w *bytes.Buffer, s string, n int) {
	io.WriteString(w, s)
	io.WriteString(w, strings.Repeat(" ", n-len(s)))
}
