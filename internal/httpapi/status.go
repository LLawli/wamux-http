package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/media"
	"github.com/LLawli/wamux-http/internal/waha"
)

// The status tag. A status is encrypted to an explicit recipient list: the
// request's contacts, or every phone contact this edge knows. The list is
// stored so the status can be revoked to the same devices. Voice statuses
// and new-message-id stay unregistered (501): the core posts text, image
// and video only and cannot take a client-made id.
func (s *Server) registerStatus(r chi.Router) {
	rt := Route{Action: ActSend, From: FromPath, Working: true}
	s.route(r, http.MethodPost, "/api/{session}/status/text", rt, s.statusText)
	s.route(r, http.MethodPost, "/api/{session}/status/image", rt, s.statusMedia(media.KindImage))
	s.route(r, http.MethodPost, "/api/{session}/status/video", rt, s.statusMedia(media.KindVideo))
	s.route(r, http.MethodPost, "/api/{session}/status/delete", rt, s.statusDelete)
}

var statusChat = &pb.Jid{Value: "status@broadcast"}

// defaultStatusBackground is WAHA's documented backgroundColor.
const defaultStatusBackground = "#38b42f"

// statusRecipients is the request's contacts as JIDs or, when it names
// none, every `@c.us` contact in the store.
func (s *Server) statusRecipients(ctx context.Context, session string, contacts []string) ([]*pb.Jid, error) {
	if len(contacts) > 0 {
		return jids(contacts, "contact")
	}
	all, err := s.allContacts(ctx, session)
	if err != nil {
		return nil, err
	}
	var out []*pb.Jid
	for _, ct := range all {
		if strings.HasSuffix(ct.ID, "@c.us") {
			if j, err := waha.ToPB(ct.ID); err == nil {
				out = append(out, j)
			}
		}
	}
	return out, nil
}

// postedTo records a status's recipients for its revoke; a failure only
// costs the revoke its default list, so it is logged.
func (s *Server) postedTo(c *Ctx, key *pb.MessageKey, to []*pb.Jid) {
	list := make([]string, len(to))
	for i, j := range to {
		list[i] = j.GetValue()
	}
	if err := s.Store.PutStatusRecipients(c.R.Context(), c.Session, key.GetId(), list); err != nil {
		s.Log.Error("store status recipients", "session", c.Session, "id", key.GetId(), "err", err)
	}
}

// argb reads a CSS hex color (#rgb, #rrggbb or #rrggbbaa) as 0xAARRGGBB.
func argb(color string) (uint32, error) {
	h := strings.TrimPrefix(strings.TrimSpace(color), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) == 6 {
		h += "ff"
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil || len(h) != 8 {
		return 0, ErrBadRequest(fmt.Sprintf("backgroundColor %q is not a hex color", color))
	}
	rgba := uint32(v)
	return rgba>>8 | rgba<<24, nil
}

func (s *Server) statusText(c *Ctx) error {
	var req struct {
		Contacts        []string `json:"contacts"`
		Text            string   `json:"text"`
		BackgroundColor string   `json:"backgroundColor"`
		Font            int32    `json:"font"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Text == "" {
		return ErrBadRequest("text is required")
	}
	if req.BackgroundColor == "" {
		req.BackgroundColor = defaultStatusBackground
	}
	bg, err := argb(req.BackgroundColor)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	to, err := s.statusRecipients(c.R.Context(), c.Session, req.Contacts)
	if err != nil {
		return err
	}
	res, err := s.Core.Messaging.PostStatusText(c.R.Context(), &pb.PostStatusTextRequest{
		Account: acc, Text: req.Text, BackgroundArgb: bg, Font: req.Font, Recipients: to,
	})
	if err != nil {
		return err
	}
	s.postedTo(c, res.GetKey(), to)
	return c.JSON(http.StatusCreated, sent(c, statusChat, res.GetKey(), res))
}

// statusMedia posts an image or a video status, with a JPEG thumbnail made
// here (the core does not transcode).
func (s *Server) statusMedia(kind media.Kind) Handler {
	return func(c *Ctx) error {
		var req struct {
			Contacts []string    `json:"contacts"`
			File     *media.File `json:"file"`
			Caption  string      `json:"caption"`
			Convert  bool        `json:"convert"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		if req.File == nil {
			return ErrBadRequest("file is required")
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		ctx := c.R.Context()
		to, err := s.statusRecipients(ctx, c.Session, req.Contacts)
		if err != nil {
			return err
		}
		blob, err := s.Media.Load(ctx, req.File, kind)
		if err != nil {
			return mediaError(err)
		}
		hdr := &pb.PostStatusMediaHeader{Account: acc, MimeType: blob.Mimetype, Caption: req.Caption, Recipients: to}
		conv := s.Media.Converter()
		if kind == media.KindVideo {
			hdr.MediaType = pb.MediaType_MEDIA_TYPE_VIDEO
			if req.Convert {
				if blob.Data, err = conv.Video(ctx, blob.Data); err != nil {
					return err
				}
				hdr.MimeType = media.MimeVideo
			}
			if secs, err := conv.Duration(ctx, blob.Data); err == nil {
				hdr.Seconds = secs
			}
		} else {
			hdr.MediaType = pb.MediaType_MEDIA_TYPE_IMAGE
		}
		hdr.Thumbnail = s.Media.Thumbnail(ctx, blob.Data)
		stream, err := s.Core.Messaging.PostStatusMedia(ctx)
		if err != nil {
			return err
		}
		if err := sendChunks(blob.Data, func(chunk []byte) error {
			return stream.Send(&pb.PostStatusMediaChunk{Part: &pb.PostStatusMediaChunk_Chunk{Chunk: chunk}})
		}, func() error {
			return stream.Send(&pb.PostStatusMediaChunk{Part: &pb.PostStatusMediaChunk_Header{Header: hdr}})
		}); err != nil {
			return err
		}
		res, err := stream.CloseAndRecv()
		if err != nil {
			return err
		}
		s.postedTo(c, res.GetKey(), to)
		return c.JSON(http.StatusCreated, sent(c, statusChat, res.GetKey(), res))
	}
}

// statusDelete revokes a status, to the devices it was posted to: the stored
// list, else the request's contacts, else every contact.
func (s *Server) statusDelete(c *Ctx) error {
	var req struct {
		ID       string   `json:"id"`
		Contacts []string `json:"contacts"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.ID == "" {
		return ErrBadRequest("id is required")
	}
	id, err := bareID(req.ID)
	if err != nil {
		return err
	}
	acc, err := c.Account()
	if err != nil {
		return err
	}
	ctx := c.R.Context()
	var to []*pb.Jid
	if posted, err := s.Store.StatusRecipients(ctx, c.Session, id); err == nil && len(posted) > 0 {
		for _, j := range posted {
			to = append(to, &pb.Jid{Value: j})
		}
	} else if to, err = s.statusRecipients(ctx, c.Session, req.Contacts); err != nil {
		return err
	}
	res, err := s.Core.Messaging.RevokeStatus(ctx, &pb.RevokeStatusRequest{Account: acc, MessageId: id, Recipients: to})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, sent(c, statusChat, res.GetKey(), res))
}
