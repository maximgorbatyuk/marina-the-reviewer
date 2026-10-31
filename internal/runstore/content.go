// Package runstore protects private review artifacts with ownership checks, bounded reads, and atomic writes.
package runstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaximumContentPartBytes bounds individual content parts so large bundles remain readable with limited memory.
const MaximumContentPartBytes = 4 << 20
const contentContextChunkBytes = 64 << 10

// MaximumBundleBytes bounds total stored content to reject unbounded bundle growth.
const MaximumBundleBytes int64 = 1 << 30

// MaximumContentParts bounds the number of content references accepted by bundle storage.
const MaximumContentParts = 4096

// ContentPart binds a stored content segment to its digest and uncompressed size.
type ContentPart struct {
	Sha256     string `json:"sha256"`
	ByteLength int64  `json:"byteLength"`
	File       string `json:"file"`
}

// ContentRef describes how verified parts reconstruct one stored content value.
type ContentRef struct {
	Sha256     string        `json:"sha256"`
	ByteLength int64         `json:"byteLength"`
	Parts      []ContentPart `json:"parts"`
}

// ContentStore stores private content by digest to avoid duplicating large review inputs.
type ContentStore struct {
	Directory   string
	uniqueBytes int64
	partCount   int
	Limits      ContentLimits
}

// ContentLimits bounds individual parts and total bundle content before it is accepted.
type ContentLimits struct {
	MaximumUniqueBytes int64 `json:"maximumUniqueBytes"`
	MaximumParts       int   `json:"maximumParts"`
}

// DefaultContentLimits provides bounded storage limits for normal review artifacts.
func DefaultContentLimits() ContentLimits {
	return ContentLimits{MaximumBundleBytes, MaximumContentParts}
}

// NewContentStore uses the default storage limits when opening private content storage.
func NewContentStore(directory string) (*ContentStore, error) {
	return NewContentStoreWithLimits(directory, DefaultContentLimits())
}

// NewContentStoreWithLimits opens private content storage with explicit resource limits.
func NewContentStoreWithLimits(directory string, limits ContentLimits) (*ContentStore, error) {
	if limits.MaximumUniqueBytes < 1 || limits.MaximumParts < 1 {
		return nil, errors.New("bundle resource limits must be positive")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{directory, filepath.Join(directory, "content")} {
		if err = os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
		info, e := os.Lstat(path)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !owned(info) {
			return nil, errors.New("bundle directory must be private and owned")
		}
	}
	store := &ContentStore{Directory: directory, Limits: limits}
	entries, err := os.ReadDir(filepath.Join(directory, "content"))
	if err != nil {
		return nil, err
	}
	if len(entries) > limits.MaximumParts {
		return nil, &ContentLimitError{"parts", int64(len(entries)), int64(limits.MaximumParts)}
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !owned(info) || info.Size() > MaximumContentPartBytes || !strings.HasSuffix(entry.Name(), ".bin") || !validHash(strings.TrimSuffix(entry.Name(), ".bin")) {
			return nil, errors.New("bundle content must be a private regular part file")
		}
		store.uniqueBytes += info.Size()
		store.partCount++
	}
	if store.uniqueBytes > limits.MaximumUniqueBytes {
		return nil, &ContentLimitError{"uniqueBytes", store.uniqueBytes, limits.MaximumUniqueBytes}
	}
	return store, nil
}

// Put uses a background context for callers without cancellation support.
func (s *ContentStore) Put(input io.Reader) (ContentRef, error) {
	return s.PutContext(context.Background(), input)
}

// PutContext stores content parts with digests so readers can verify reconstructed bytes.
func (s *ContentStore) PutContext(ctx context.Context, input io.Reader) (ContentRef, error) {
	if err := ctx.Err(); err != nil {
		return ContentRef{}, err
	}
	ref := ContentRef{Parts: []ContentPart{}}
	digest := sha256.New()
	bufferSize := MaximumContentPartBytes
	if known, ok := input.(interface{ Len() int }); ok {
		bufferSize = min(bufferSize, known.Len())
	}
	if bufferSize == 0 {
		if err := ctx.Err(); err != nil {
			return ContentRef{}, err
		}
		ref.Sha256 = hex.EncodeToString(digest.Sum(nil))
		return ref, nil
	}
	buffer := make([]byte, bufferSize)
	for {
		n, err := io.ReadFull(&contentContextReader{ctx: ctx, reader: input}, buffer)
		if cancellation := ctx.Err(); cancellation != nil {
			return ContentRef{}, cancellation
		}
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return ContentRef{}, err
		}
		if n > 0 {
			if len(ref.Parts) >= s.Limits.MaximumParts {
				return ContentRef{}, &ContentLimitError{"contentParts", int64(len(ref.Parts) + 1), int64(s.Limits.MaximumParts)}
			}
			raw := buffer[:n]
			partDigest := sha256.New()
			if e := writeContentDigests(ctx, raw, partDigest, digest); e != nil {
				return ContentRef{}, e
			}
			h := hex.EncodeToString(partDigest.Sum(nil))
			part := ContentPart{h, int64(n), "content/" + h + ".bin"}
			path := filepath.Join(s.Directory, part.File)
			if existing, e := ReadPrivateContext(ctx, path, MaximumContentPartBytes); e == nil {
				if !bytes.Equal(existing, raw) {
					return ContentRef{}, errors.New("saved bundle content is corrupt")
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return ContentRef{}, e
			} else {
				if s.partCount >= s.Limits.MaximumParts {
					return ContentRef{}, &ContentLimitError{"parts", int64(s.partCount + 1), int64(s.Limits.MaximumParts)}
				}
				if s.uniqueBytes+int64(n) > s.Limits.MaximumUniqueBytes {
					return ContentRef{}, &ContentLimitError{"uniqueBytes", s.uniqueBytes + int64(n), s.Limits.MaximumUniqueBytes}
				}
				if e = AtomicBytesContext(ctx, path, raw); e != nil {
					return ContentRef{}, e
				}
				s.uniqueBytes += int64(n)
				s.partCount++
			}
			ref.ByteLength += int64(n)
			ref.Parts = append(ref.Parts, part)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return ContentRef{}, err
	}
	ref.Sha256 = hex.EncodeToString(digest.Sum(nil))
	return ref, nil
}

// PutText uses a background context for callers without cancellation support.
func (s *ContentStore) PutText(value string) (ContentRef, error) {
	return s.PutTextContext(context.Background(), value)
}

// PutTextContext stores text through the same verified content path used by other artifacts.
func (s *ContentStore) PutTextContext(ctx context.Context, value string) (ContentRef, error) {
	return s.PutContext(ctx, strings.NewReader(value))
}

// PutJSON uses a background context for callers without cancellation support.
func (s *ContentStore) PutJSON(value any) (ContentRef, error) {
	return s.PutJSONContext(context.Background(), value)
}

// PutJSONContext serializes a document before placing it in verified content storage.
func (s *ContentStore) PutJSONContext(ctx context.Context, value any) (ContentRef, error) {
	if err := ctx.Err(); err != nil {
		return ContentRef{}, err
	}
	raw, err := json.Marshal(value)
	if cancellation := ctx.Err(); cancellation != nil {
		return ContentRef{}, cancellation
	}
	if err != nil {
		return ContentRef{}, err
	}
	return s.PutContext(ctx, bytes.NewReader(append(raw, '\n')))
}

// Reader uses a background context for callers without cancellation support.
func (s *ContentStore) Reader(ref ContentRef) (io.ReadCloser, error) {
	return s.ReaderContext(context.Background(), ref)
}

// ReaderContext streams verified content parts without requiring a full in-memory copy.
func (s *ContentStore) ReaderContext(ctx context.Context, ref ContentRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validHash(ref.Sha256) || ref.ByteLength < 0 || len(ref.Parts) > s.Limits.MaximumParts {
		return nil, errors.New("bundle content reference is invalid")
	}
	var total int64
	for _, part := range ref.Parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !validHash(part.Sha256) || part.ByteLength < 1 || part.ByteLength > MaximumContentPartBytes || part.File != "content/"+part.Sha256+".bin" {
			return nil, errors.New("bundle part reference is invalid")
		}
		total += part.ByteLength
	}
	if total != ref.ByteLength {
		return nil, errors.New("bundle part lengths do not match the content length")
	}
	return &contentReader{ctx: ctx, store: s, ref: ref, digest: sha256.New()}, nil
}

// Bytes uses a background context for callers without cancellation support.
func (s *ContentStore) Bytes(ref ContentRef) ([]byte, error) {
	return s.BytesContext(context.Background(), ref)
}

// BytesContext reconstructs content only after its references and digests are verified.
func (s *ContentStore) BytesContext(ctx context.Context, ref ContentRef) ([]byte, error) {
	r, err := s.ReaderContext(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// Decode uses a background context for callers without cancellation support.
func (s *ContentStore) Decode(ref ContentRef, value any) error {
	return s.DecodeContext(context.Background(), ref, value)
}

// DecodeContext decodes a document from verified stored content with caller cancellation.
func (s *ContentStore) DecodeContext(ctx context.Context, ref ContentRef, value any) error {
	r, err := s.ReaderContext(ctx, ref)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(r)
	if err = decoder.Decode(value); err == nil {
		var extra any
		if e := decoder.Decode(&extra); e != io.EOF {
			if cancellation := ctx.Err(); cancellation != nil {
				err = cancellation
			} else {
				err = errors.New("bundle JSON has trailing data")
			}
		}
	}
	if e := r.Close(); err == nil {
		err = e
	}
	return err
}

type contentReader struct {
	ctx      context.Context
	store    *ContentStore
	ref      ContentRef
	index    int
	current  *bytes.Reader
	digest   hash.Hash
	count    int64
	verified bool
}

func (r *contentReader) Read(buffer []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.current != nil {
			n, err := r.current.Read(buffer)
			if n > 0 {
				_, _ = r.digest.Write(buffer[:n])
				r.count += int64(n)
				return n, nil
			}
			if err != io.EOF {
				return n, err
			}
			r.current = nil
		}
		if r.index == len(r.ref.Parts) {
			if r.count != r.ref.ByteLength || hex.EncodeToString(r.digest.Sum(nil)) != r.ref.Sha256 {
				return 0, errors.New("bundle content digest does not match")
			}
			r.verified = true
			return 0, io.EOF
		}
		part := r.ref.Parts[r.index]
		r.index++
		data, err := ReadPrivateContext(r.ctx, filepath.Join(r.store.Directory, part.File), MaximumContentPartBytes)
		if err != nil {
			return 0, err
		}
		partDigest := sha256.New()
		if err := writeContentDigests(r.ctx, data, partDigest); err != nil {
			return 0, err
		}
		if int64(len(data)) != part.ByteLength || hex.EncodeToString(partDigest.Sum(nil)) != part.Sha256 {
			return 0, errors.New("bundle part digest or length does not match")
		}
		r.current = bytes.NewReader(data)
	}
}

func (r *contentReader) Close() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.verified {
		return nil
	}
	_, err := io.Copy(io.Discard, r)
	return err
}

func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == hex.EncodeToString(decoded)
}

// ContentLimitError separates resource limit failures from invalid content or storage errors.
type ContentLimitError struct {
	Resource string
	Actual   int64
	Allowed  int64
}

func (e *ContentLimitError) Error() string {
	return fmt.Sprintf("bundle resource limit exceeded: resource=%s actual=%d allowed=%d", e.Resource, e.Actual, e.Allowed)
}

// AtomicBytes uses a background context for callers without cancellation support.
func AtomicBytes(path string, raw []byte) error {
	return AtomicBytesContext(context.Background(), path, raw)
}

// AtomicBytesContext replaces a private file only after the complete content has been written and synced.
func AtomicBytesContext(ctx context.Context, path string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("output must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(parent, ".marinadoreview-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	writer := &contentContextWriter{ctx: ctx, writer: f}
	if _, err = writer.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

type contentContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contentContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(data[:min(len(data), contentContextChunkBytes)])
	if cancellation := r.ctx.Err(); cancellation != nil {
		return n, cancellation
	}
	return n, err
}

type contentContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w *contentContextWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		if err := w.ctx.Err(); err != nil {
			return written, err
		}
		n, err := w.writer.Write(data[:min(len(data), contentContextChunkBytes)])
		written += n
		data = data[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, w.ctx.Err()
}

func writeContentDigests(ctx context.Context, raw []byte, digests ...hash.Hash) error {
	for len(raw) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := min(len(raw), contentContextChunkBytes)
		for _, digest := range digests {
			_, _ = digest.Write(raw[:size])
		}
		raw = raw[size:]
	}
	return ctx.Err()
}

// ReadPrivateContext reads bounded private files while honoring caller cancellation.
func ReadPrivateContext(ctx context.Context, path string, maximum int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := ReadPrivate(path, maximum)
	if cancellation := ctx.Err(); cancellation != nil {
		return nil, cancellation
	}
	return raw, err
}
