package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

// PUT /profile/status (the about text) has no core RPC and answers 501.
func (s *Server) registerProfile(r chi.Router) {
	read := Route{Action: ActRead, From: FromPath, Working: true}
	write := Route{Action: ActSend, From: FromPath, Working: true}

	s.route(r, "GET", "/api/{session}/profile", read, func(c *Ctx) error {
		sess, err := c.Sess()
		if err != nil {
			return err
		}
		var me struct {
			ID       string `json:"id"`
			PushName string `json:"pushName"`
		}
		_ = json.Unmarshal(sess.Me, &me)
		if me.ID == "" {
			return ErrUnprocessable("No profile found")
		}
		acc, jid, err := s.dirAccountJID(c, me.ID)
		if err != nil {
			return err
		}
		ctx := c.R.Context()
		name, err := s.Core.Contacts.GetPushName(ctx, acc)
		if err != nil {
			return err
		}
		if name.GetPushName() != "" {
			me.PushName = name.GetPushName()
		}
		picture, err := s.dirPictureURL(ctx, acc, jid)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"id": me.ID, "name": me.PushName, "picture": picture})
	})

	s.route(r, "PUT", "/api/{session}/profile/name", write, func(c *Ctx) error {
		var body map[string]any
		if err := c.Bind(&body); err != nil {
			return err
		}
		name, isString := body["name"].(string)
		var msgs []string
		if strings.TrimSpace(name) == "" {
			msgs = append(msgs, "name should not be empty")
		}
		if !isString {
			msgs = append(msgs, "name must be a string")
		}
		if len(msgs) > 0 {
			return ErrBadRequest(msgs)
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Contacts.SetPushName(c.R.Context(), &pb.SetPushNameRequest{Account: acc, PushName: name}); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	})

	s.route(r, "PUT", "/api/{session}/profile/picture", write, func(c *Ctx) error {
		image, err := dirFile(c)
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Contacts.SetProfilePicture(c.R.Context(), &pb.SetProfilePictureRequest{Account: acc, Image: image}); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	})

	s.route(r, "DELETE", "/api/{session}/profile/picture", write, func(c *Ctx) error {
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Contacts.RemoveProfilePicture(c.R.Context(), acc); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	})
}
