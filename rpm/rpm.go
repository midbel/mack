package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/midbel/mack"
	"github.com/midbel/tape"
	"github.com/midbel/tape/cpio"
)

const (
	SigTypeRPM = 5
	MajorRPM   = 3
	MinorRPM   = 0
	MagicRPM   = 0xedabeedb
	MagicHDR   = 0x008eade8
)

const (
	SigBase        = 256
	SigLength      = 1000
	SigPGP         = 1002
	SigMD5         = 1004
	SigSha1        = SigBase + 13
	SigPayloadSize = 1007
)

const (
	TagPackage = 1000
	TagVersion = 1001
	TagRelease = 1002
	TagSummary = 1004
	TagDesc    = 1005
	TagVendor  = 1011
	TagLicense = 1014
	TagURL     = 1020
)

type builder struct {
	inner io.Writer

	md5sums   []string
	filenames []string
	sizes     []int64
}

func NewBuilder(w io.Writer) mack.Builder {
	return &builder{inner: w}
}

func (w *builder) Build(c mack.Control, files []*mack.File) error {
	e := Lead{
		Major:   MajorRPM,
		Minor:   MinorRPM,
		SigType: SigTypeRPM,
		Name:    c.Package,
	}
	if err := writeLead(w.inner, e); err != nil {
		return err
	}
	meta, err := writeMetadata(&c)
	if err != nil {
		return err
	}
	body, err := w.writeArchive(files)
	if err != nil {
		return err
	}

	var data bytes.Buffer
	md5sum, shasum := md5.New(), sha1.New()
	if _, err := io.Copy(io.MultiWriter(&data, md5sum, shasum), io.MultiReader(meta, body)); err != nil {
		return err
	}
	fs := []Field{
		number{tag: SigLength, kind: int32(Int32), Value: int64(data.Len())},
		binarray{tag: SigMD5, Value: md5sum.Sum(nil)},
		binarray{tag: SigSha1, Value: shasum.Sum(nil)},
	}
	sig, err := writeFields(fs, true)
	if err != nil {
		return nil
	}
	_, err = io.Copy(w.inner, io.MultiReader(sig, &data))
	return err
}

func writeFields(fs []Field, pad bool) (*bytes.Buffer, error) {
	var meta, body, store bytes.Buffer
	var i int32
	for _, f := range fs {
		fmt.Println(f)
		if f.Skip() {
			continue
		}
		i++

		binary.Write(&body, binary.BigEndian, f.Tag())
		binary.Write(&body, binary.BigEndian, f.Type())
		binary.Write(&body, binary.BigEndian, int32(store.Len()))
		binary.Write(&body, binary.BigEndian, f.Len())
		store.Write(f.Bytes())
	}
	binary.Write(&meta, binary.BigEndian, uint32((MagicHDR<<8)|1))
	binary.Write(&meta, binary.BigEndian, uint32(0))
	binary.Write(&meta, binary.BigEndian, i)
	binary.Write(&meta, binary.BigEndian, int32(store.Len()))

	_, err := io.Copy(&meta, io.MultiReader(&body, &store))
	if mod := meta.Len() % 8; mod != 0 && pad {
		bs := make([]byte, 8-mod)
		meta.Write(bs)
	}
	return &meta, err
}

func (w *builder) writeArchive(files []*mack.File) (*bytes.Buffer, error) {
	body := new(bytes.Buffer)
	ark := cpio.NewWriter(body)
	for _, f := range files {
		bs, err := writeFile(ark, f)
		if err != nil {
			return nil, err
		}
		w.sizes = append(w.sizes, int64(len(bs)))
		w.md5sums = append(w.md5sums, fmt.Sprintf("%x", bs))
		w.filenames = append(w.filenames, f.String())
	}
	if err := ark.Close(); err != nil {
		return nil, err
	}
	bz := new(bytes.Buffer)
	gz, _ := gzip.NewWriterLevel(bz, gzip.BestCompression)
	if _, err := io.Copy(gz, body); err != nil {
		return nil, err
	}
	return bz, nil
}

func writeFile(w *cpio.Writer, f *mack.File) ([]byte, error) {
	r, err := os.Open(f.Src)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	i, err := r.Stat()
	if err != nil {
		return nil, err
	}
	h := tape.Header{
		Filename: f.String(),
		Mode:     int64(i.Mode()),
		Length:   i.Size(),
		ModTime:  i.ModTime(),
	}
	if err := w.WriteHeader(&h); err != nil {
		return nil, err
	}
	s := md5.New()
	if _, err := io.Copy(w, io.TeeReader(r, s)); err != nil {
		return nil, err
	}
	return s.Sum(nil), err
}

type Field interface {
	Tag() int32
	Type() int32
	Len() int32
	Skip() bool
	Bytes() []byte
}

type binarray struct {
	tag   int32
	Value []byte
}

func (b binarray) Skip() bool    { return len(b.Value) == 0 }
func (b binarray) Tag() int32    { return b.tag }
func (b binarray) Type() int32   { return int32(Binary) }
func (b binarray) Len() int32    { return int32(len(b.Value)) }
func (b binarray) Bytes() []byte { return b.Value }

type number struct {
	tag   int32
	kind  int32
	Value int64
}

func (n number) Skip() bool  { return false }
func (n number) Tag() int32  { return n.tag }
func (n number) Type() int32 { return n.kind }
func (n number) Len() int32  { return 1 }
func (n number) Bytes() []byte {
	var w bytes.Buffer
	switch EntryType(n.kind) {
	case Int8:
		binary.Write(&w, binary.BigEndian, int8(n.Value))
	case Int16:
		binary.Write(&w, binary.BigEndian, int16(n.Value))
	case Int32:
		binary.Write(&w, binary.BigEndian, int32(n.Value))
	case Int64:
		binary.Write(&w, binary.BigEndian, int64(n.Value))
	}
	return w.Bytes()
}

type varchar struct {
	tag   int32
	Value string
}

func (v varchar) Skip() bool  { return len(v.Value) == 0 }
func (v varchar) Tag() int32  { return v.tag }
func (v varchar) Type() int32 { return int32(String) }
func (v varchar) Len() int32  { return 1 }
func (v varchar) Bytes() []byte {
	return append([]byte(v.Value), 0)
}

func controlToFields(c *mack.Control) []Field {
	var fs []Field

	fs = append(fs, varchar{tag: TagPackage, Value: c.Package})
	fs = append(fs, varchar{tag: TagVersion, Value: c.Version})
	fs = append(fs, varchar{tag: TagSummary, Value: c.Summary})
	fs = append(fs, varchar{tag: TagDesc, Value: c.Desc})
	fs = append(fs, varchar{tag: TagVendor, Value: c.Vendor})
	fs = append(fs, varchar{tag: TagLicense, Value: c.License})
	fs = append(fs, varchar{tag: TagURL, Value: c.Home})

	return fs
}

func writeMetadata(c *mack.Control) (*bytes.Buffer, error) {
	fs := controlToFields(c)
	return writeFields(fs, false)
}

func writeLead(w io.Writer, e Lead) error {
	body := new(bytes.Buffer)
	binary.Write(body, binary.BigEndian, uint32(MagicRPM))
	binary.Write(body, binary.BigEndian, e.Major)
	binary.Write(body, binary.BigEndian, e.Minor)
	binary.Write(body, binary.BigEndian, e.Type)
	binary.Write(body, binary.BigEndian, e.Arch)
	if n := e.Name; len(n) > 66 {
		io.WriteString(body, n[:66])
	} else {
		bs := make([]byte, 66-len(n))
		vs := append([]byte(n), bs...)
		body.Write(vs)
	}

	binary.Write(body, binary.BigEndian, e.Os)
	binary.Write(body, binary.BigEndian, e.SigType)
	for i := 0; i < 4; i++ {
		binary.Write(body, binary.BigEndian, uint32(0))
	}

	_, err := io.Copy(w, body)
	return err
}
