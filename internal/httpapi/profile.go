package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
)

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
		v, err := requiredStrings(c, "name")
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Contacts.SetPushName(c.R.Context(), &pb.SetPushNameRequest{Account: acc, PushName: v["name"]}); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]bool{"success": true})
	})

	s.route(r, "PUT", "/api/{session}/profile/status", write, func(c *Ctx) error {
		v, err := requiredStrings(c, "status")
		if err != nil {
			return err
		}
		acc, err := c.Account()
		if err != nil {
			return err
		}
		if _, err := s.Core.Contacts.SetAbout(c.R.Context(), &pb.SetAboutRequest{Account: acc, About: v["status"]}); err != nil {
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

// requiredStrings reads the body's fields as WAHA's DTOs check them
// (@IsString, @IsNotEmpty, so "   " passes and 5 is only not a string): a
// 400 listing every field that is missing, empty or not a string.
func requiredStrings(c *Ctx, fields ...string) (map[string]string, error) {
	var body map[string]any
	if err := c.Bind(&body); err != nil {
		return nil, err
	}
	out := map[string]string{}
	var msgs []string
	for _, f := range fields {
		raw, present := body[f]
		v, isString := raw.(string)
		if !present || raw == nil || (isString && v == "") {
			msgs = append(msgs, f+" should not be empty")
		}
		if !isString {
			msgs = append(msgs, f+" must be a string")
		}
		out[f] = v
	}
	if len(msgs) > 0 {
		return nil, ErrBadRequest(msgs)
	}
	return out, nil
}
