package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

const multipartOverheadLimit int64 = 1 << 20

type fileService struct {
	root    string
	maxSize int64
}

func (s *Server) fileUploadRate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := agentFrom(r.Context())
		if !s.allow("upload:"+a.ID, s.opts.FileUploadsPerMinute, s.opts.FileUploadsPerMinute) {
			w.Header().Set("Retry-After", "6")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "file upload rate exceeded", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.files.maxSize > int64(^uint64(0)>>1)-multipartOverheadLimit {
		writeError(w, http.StatusInternalServerError, "internal", "invalid file size configuration", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.files.maxSize+multipartOverheadLimit)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "expected multipart/form-data with one file field", nil)
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		writeMultipartError(w, err)
		return
	}
	if part.FormName() != "file" || part.FileName() == "" {
		_ = part.Close()
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart field 'file' is required", nil)
		return
	}
	name := sanitizeUploadName(part.FileName())
	if err = os.MkdirAll(filepath.Join(s.files.root, ".tmp"), 0o700); err != nil {
		_ = part.Close()
		s.internalError(w, err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Join(s.files.root, ".tmp"), "upload-*")
	if err != nil {
		_ = part.Close()
		s.internalError(w, err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(part, s.files.maxSize+1))
	partCloseErr := part.Close()
	if err != nil {
		writeMultipartError(w, err)
		return
	}
	if partCloseErr != nil {
		writeMultipartError(w, partCloseErr)
		return
	}
	if size > s.files.maxSize {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "file is too large", map[string]any{"max_bytes": s.files.maxSize})
		return
	}
	if _, err = mr.NextPart(); err == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "exactly one multipart file field is allowed", nil)
		return
	} else if !errors.Is(err, io.EOF) {
		writeMultipartError(w, err)
		return
	}
	if err = tmp.Sync(); err != nil {
		s.internalError(w, err)
		return
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		s.internalError(w, err)
		return
	}
	var header [512]byte
	n, readErr := tmp.Read(header[:])
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		s.internalError(w, readErr)
		return
	}
	mimeType := http.DetectContentType(header[:n])
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		s.internalError(w, err)
		return
	}
	if err = tmp.Close(); err != nil {
		s.internalError(w, err)
		return
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	blobPath, relativePath, err := s.files.blobPath(digest)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(blobPath), 0o700); err != nil {
		s.internalError(w, err)
		return
	}
	if err = installBlob(tmpPath, blobPath, size, digest); err != nil {
		s.internalError(w, err)
		return
	}
	ref := protocol.FileRef{ID: "fil_" + protocol.NewID(), Name: name, Size: size, SHA256: digest, Mime: mimeType}
	stored, _, err := s.store.RegisterFile(r.Context(), ref, relativePath)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, stored.Ref)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, err := s.store.FileByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrFileNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "file not found", nil)
		} else {
			s.internalError(w, err)
		}
		return
	}
	allowed, err := s.store.CanAccessFile(r.Context(), agentFrom(r.Context()).ID, id)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "not_found", "file not found", nil)
		return
	}
	path, _, err := s.files.blobPath(stored.Ref.SHA256)
	if err != nil {
		s.internalError(w, err)
		return
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "not_found", "file not found", nil)
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !info.Mode().IsRegular() || info.Size() != stored.Ref.Size {
		s.internalError(w, fmt.Errorf("file blob integrity check failed for %s", id))
		return
	}
	if _, err = hex.DecodeString(stored.Ref.SHA256); err != nil || len(stored.Ref.SHA256) != sha256.Size*2 {
		s.internalError(w, fmt.Errorf("invalid stored file digest for %s", id))
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": stored.Ref.Name})
	if disposition == "" {
		disposition = `attachment; filename="download.bin"`
	}
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-SHA256", stored.Ref.SHA256)
	if stored.Ref.Mime != "" {
		w.Header().Set("Content-Type", stored.Ref.Mime)
	}
	http.ServeContent(w, r, stored.Ref.Name, info.ModTime(), f)
}

func (f *fileService) blobPath(digest string) (string, string, error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
		return "", "", errors.New("invalid SHA-256 digest")
	}
	relative := filepath.Join(digest[:2], digest)
	return filepath.Join(f.root, relative), relative, nil
}

func installBlob(tempPath, blobPath string, size int64, digest string) error {
	if err := os.Link(tempPath, blobPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	existing, err := os.Open(blobPath)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	gotSize, copyErr := io.Copy(hasher, existing)
	closeErr := existing.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if gotSize != size || hex.EncodeToString(hasher.Sum(nil)) != digest {
		return errors.New("existing content-addressed blob failed integrity check")
	}
	return nil
}

func sanitizeUploadName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	runes := make([]rune, 0, len(name))
	for _, r := range name {
		if !unicode.IsControl(r) {
			runes = append(runes, r)
		}
	}
	if len(runes) > 255 {
		runes = runes[:255]
	}
	name = strings.TrimSpace(string(runes))
	if name == "" || name == "." || name == ".." {
		return "upload.bin"
	}
	return name
}

func writeMultipartError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "upload request is too large", nil)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "invalid multipart upload", nil)
}
