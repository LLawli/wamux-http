package waha

import (
	"sort"
	"strings"
)

// extensions maps a mimetype to the extension a file of that type is
// served under: the first extension the mime-db lists for it, as WAHA's
// `mime-types` picks it. A fixed table, never the host's mime files, so
// every edge process names a file the same way and the link one process
// puts in a message is the file another serves.
var extensions = map[string]string{
	"image/jpeg":                    "jpeg",
	"image/png":                     "png",
	"image/gif":                     "gif",
	"image/webp":                    "webp",
	"image/heic":                    "heic",
	"image/heif":                    "heif",
	"image/avif":                    "avif",
	"image/bmp":                     "bmp",
	"image/svg+xml":                 "svg",
	"image/tiff":                    "tif",
	"audio/ogg":                     "oga",
	"audio/opus":                    "opus",
	"audio/mpeg":                    "mpga",
	"audio/mp4":                     "m4a",
	"audio/aac":                     "aac",
	"audio/amr":                     "amr",
	"audio/wav":                     "wav",
	"audio/x-wav":                   "wav",
	"audio/webm":                    "weba",
	"audio/flac":                    "flac",
	"video/mp4":                     "mp4",
	"video/3gpp":                    "3gp",
	"video/quicktime":               "qt",
	"video/webm":                    "webm",
	"video/mpeg":                    "mpeg",
	"video/x-matroska":              "mkv",
	"video/x-msvideo":               "avi",
	"text/plain":                    "txt",
	"text/csv":                      "csv",
	"text/html":                     "html",
	"text/vcard":                    "vcard",
	"text/x-vcard":                  "vcf",
	"application/pdf":               "pdf",
	"application/zip":               "zip",
	"application/was":               "zip", // a sticker pack
	"application/json":              "json",
	"application/xml":               "xml",
	"application/gzip":              "gz",
	"application/x-tar":             "tar",
	"application/x-7z-compressed":   "7z",
	"application/vnd.rar":           "rar",
	"application/x-rar-compressed":  "rar",
	"application/rtf":               "rtf",
	"application/msword":            "doc",
	"application/vnd.ms-excel":      "xls",
	"application/vnd.ms-powerpoint": "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.oasis.opendocument.text":                                   "odt",
	"application/vnd.oasis.opendocument.spreadsheet":                            "ods",
	"application/vnd.oasis.opendocument.presentation":                           "odp",
	"application/vnd.android.package-archive":                                   "apk",
	"application/octet-stream":                                                  "bin",
}

// MediaExtension is the extension for a mimetype ("audio/ogg; codecs=opus"
// gives "oga"); "bin" for a type the table does not know.
func MediaExtension(mimetype string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(mimetype, ";", 2)[0]))
	if ext, ok := extensions[base]; ok {
		return ext
	}
	return "bin"
}

var byExtension = func() map[string]string {
	types := make([]string, 0, len(extensions))
	for t := range extensions {
		types = append(types, t)
	}
	sort.Strings(types) // a stable pick when two types share an extension
	out := map[string]string{}
	for _, t := range types {
		if _, taken := out[extensions[t]]; !taken {
			out[extensions[t]] = t
		}
	}
	return out
}()

// MimetypeOfExtension is the mimetype a file named with ext is served as;
// application/octet-stream for an extension the table does not know.
func MimetypeOfExtension(ext string) string {
	if t, ok := byExtension[strings.ToLower(ext)]; ok {
		return t
	}
	return "application/octet-stream"
}
