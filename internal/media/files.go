package media

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/store"
)

// ErrFileNotFound is a file name that names no media this edge can serve.
var ErrFileNotFound = errors.New("file not found")

// extensions maps a mimetype to the extension WAHA's `mime-types` gives it
// (the first one mime-db lists), so file names match WAHA's.
var extensions = map[string]string{
	"image/jpeg":                    "jpeg",
	"image/png":                     "png",
	"image/gif":                     "gif",
	"image/webp":                    "webp",
	"image/heic":                    "heic",
	"audio/ogg":                     "oga",
	"audio/mpeg":                    "mpga",
	"audio/mp4":                     "m4a",
	"audio/aac":                     "aac",
	"audio/amr":                     "amr",
	"audio/wav":                     "wav",
	"audio/webm":                    "weba",
	"video/mp4":                     "mp4",
	"video/3gpp":                    "3gp",
	"video/quicktime":               "mov",
	"video/webm":                    "webm",
	"application/pdf":               "pdf",
	"application/zip":               "zip",
	"application/was":               "zip", // a sticker pack
	"application/json":              "json",
	"application/msword":            "doc",
	"application/vnd.ms-excel":      "xls",
	"application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"text/plain": "txt",
	"text/csv":   "csv",
	"text/html":  "html",
}

// Extension is the file extension for a mimetype, without the dot; "bin"
// when nothing better is known.
func Extension(mimetype string) string {
	mt, _, err := mime.ParseMediaType(mimetype)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(mimetype))
	}
	if ext, ok := extensions[mt]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
		return strings.TrimPrefix(exts[0], ".")
	}
	return "bin"
}

// FileName is the name a received media file is served under:
// `<WhatsApp message id>.<ext>`, as in WAHA.
func FileName(waID, mimetype string) string { return waID + "." + Extension(mimetype) }

// FileURL is the public link to a received media file:
// `<base URL>/api/files/<session>/<waID>.<ext>`.
func (s *Service) FileURL(session, waID, mimetype string) string {
	return strings.TrimRight(s.Config.BaseURL, "/") + "/api/files/" + session + "/" + FileName(waID, mimetype)
}

var (
	sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,54}$`)
	fileNameRe    = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}\.[A-Za-z0-9]{1,10}$`)
)

// sweepEvery is how often serving a file also deletes expired ones.
const sweepEvery = time.Minute

// Served is a media file on disk, ready to send.
type Served struct {
	Path     string
	Mimetype string
}

// File returns the cached file for a received message's media, downloading
// it from the core on first request. The cache lives under
// FilesFolder/<session>/ and a file older than FilesLifetime is fetched
// again (and deleted by the sweep).
func (s *Service) File(ctx context.Context, session, name string) (Served, error) {
	if !sessionNameRe.MatchString(session) || !fileNameRe.MatchString(name) {
		return Served{}, ErrFileNotFound
	}
	s.maybeSweep()
	waID, ext, _ := strings.Cut(name, ".")
	path := filepath.Join(s.Config.FilesFolder, session, name)

	msg, err := s.Store.GetMessageByWAID(ctx, session, waID)
	if errors.Is(err, store.ErrNotFound) {
		// A file cached before its message was purged is still served
		// until it expires, like WAHA's static folder.
		if s.fresh(path) {
			return Served{Path: path, Mimetype: mime.TypeByExtension("." + ext)}, nil
		}
		return Served{}, ErrFileNotFound
	}
	if err != nil {
		return Served{}, err
	}
	in := &pb.InboundMessage{}
	if err := proto.Unmarshal(msg.Raw, in); err != nil || in.GetMedia() == nil {
		return Served{}, ErrFileNotFound
	}
	desc := in.GetMedia()
	if Extension(desc.GetMimeType()) != ext {
		return Served{}, ErrFileNotFound
	}
	out := Served{Path: path, Mimetype: desc.GetMimeType()}

	lock, _ := s.downloads.LoadOrStore(path, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	// Forgetting the lock before releasing it keeps the map small; a request
	// that takes a new lock meanwhile finds the file fresh below.
	defer s.downloads.Delete(path)
	if s.fresh(path) {
		return out, nil
	}
	sess, err := s.Store.GetSession(ctx, session)
	if err != nil {
		return Served{}, err
	}
	mt, err := s.download(ctx, accountRef(sess), desc, path)
	if err != nil {
		return Served{}, err
	}
	if out.Mimetype == "" {
		out.Mimetype = mt
	}
	if lt := s.Config.FilesLifetime; lt > 0 {
		time.AfterFunc(lt, func() { s.expire(path) })
	}
	return out, nil
}

func accountRef(sess store.Session) *pb.AccountRef {
	if sess.AccountUUID != "" {
		return &pb.AccountRef{Ref: &pb.AccountRef_Uuid{Uuid: sess.AccountUUID}}
	}
	return &pb.AccountRef{Ref: &pb.AccountRef_ExternalRef{ExternalRef: sess.Name}}
}

// download streams the media from the core into path (through a temp file,
// so a reader never sees half a file) and returns the core's mimetype.
func (s *Service) download(ctx context.Context, acc *pb.AccountRef, desc *pb.MediaDescriptor, path string) (string, error) {
	stream, err := s.Core.Media.DownloadMedia(ctx, &pb.DownloadMediaRequest{Account: acc, Descriptor_: desc})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	var mt string
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = tmp.Close()
			return "", err
		}
		if m := chunk.GetMeta(); m != nil {
			mt = m.GetMimeType()
		}
		if _, err := tmp.Write(chunk.GetChunk()); err != nil {
			_ = tmp.Close()
			return "", err
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return mt, os.Rename(tmp.Name(), path)
}

// fresh reports whether a cached file exists and has not expired.
func (s *Service) fresh(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	lt := s.Config.FilesLifetime
	return lt <= 0 || time.Since(st.ModTime()) < lt
}

// expire deletes a cached file once it is past its lifetime; a file
// downloaded again since is left alone.
func (s *Service) expire(path string) {
	if st, err := os.Stat(path); err == nil && !s.fresh(path) && st.Mode().IsRegular() {
		_ = os.Remove(path)
	}
}

// maybeSweep deletes expired files in the background, at most once per
// sweepEvery, so files cached by an earlier process expire too.
func (s *Service) maybeSweep() {
	if s.Config.FilesLifetime <= 0 {
		return
	}
	now := time.Now().UnixMilli()
	last := s.lastSweep.Load()
	if now-last < sweepEvery.Milliseconds() || !s.lastSweep.CompareAndSwap(last, now) {
		return
	}
	go s.Sweep()
}

// Sweep deletes every expired file in the files folder.
func (s *Service) Sweep() {
	_ = filepath.WalkDir(s.Config.FilesFolder, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			s.expire(path)
		}
		return nil
	})
}
