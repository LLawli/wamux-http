package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"

	"github.com/LLawli/wamux-http/internal/core"
	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/waha"
)

// contactsPage is how many stored contacts one store read brings while the
// list is sorted in memory.
const contactsPage = 1000

// defaultLidsLimit is WAHA's LidsListQueryParams default.
const defaultLidsLimit = 100

// `PUT /api/{session}/contacts/{chatId}` has no core RPC and answers 501.
func (s *Server) registerContacts(r chi.Router) {
	read := Route{Action: ActRead, From: FromQuery, Working: true}

	s.route(r, "GET", "/api/contacts/all", read, func(c *Ctx) error {
		p, err := dirPagination(c, "id", "name")
		if err != nil {
			return err
		}
		all, err := s.allContacts(c.R.Context(), c.Session)
		if err != nil {
			return err
		}
		key := func(x store.Contact) string { return x.ID }
		if p.SortBy == "name" {
			key = func(x store.Contact) string { return x.Name }
		}
		sort.SliceStable(all, func(i, j int) bool {
			if p.Desc {
				return key(all[i]) > key(all[j])
			}
			return key(all[i]) < key(all[j])
		})
		out := []waha.Contact{}
		for _, i := range p.apply(len(all)) {
			out = append(out, waha.NewContact(all[i].ID, all[i].Name, all[i].PushName))
		}
		return c.JSON(http.StatusOK, out)
	})

	s.route(r, "GET", "/api/contacts", read, func(c *Ctx) error {
		return s.contactInfo(c, c.Query("contactId"))
	})
	s.route(r, "GET", "/api/{session}/contacts/{id}", Route{Action: ActRead, From: FromPath, Working: true}, func(c *Ctx) error {
		return s.contactInfo(c, c.Param("id"))
	})

	s.route(r, "GET", "/api/contacts/about", read, func(c *Ctx) error {
		acc, jid, err := s.dirAccountJID(c, c.Query("contactId"))
		if err != nil {
			return err
		}
		res, err := s.Core.Contacts.GetAbout(c.R.Context(), &pb.JidRequest{Account: acc, Jid: jid})
		if dirHidden(err) {
			return c.Empty(http.StatusOK) // WAHA: null without permission to read it
		}
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"about": res.GetAbout()})
	})

	s.route(r, "GET", "/api/contacts/profile-picture", read, func(c *Ctx) error {
		if err := dirBool(c, "refresh"); err != nil {
			return err
		}
		acc, jid, err := s.dirAccountJID(c, c.Query("contactId"))
		if err != nil {
			return err
		}
		url, err := s.dirPictureURL(c.R.Context(), acc, jid)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]*string{"profilePictureURL": url})
	})

	checkNumber := func(c *Ctx) error {
		phone := strings.ReplaceAll(c.Query("phone"), "+", "") // as WAHA
		acc, jid, err := s.dirAccountJID(c, phone)
		if err != nil {
			return err
		}
		res, err := s.Core.Contacts.CheckOnWhatsApp(c.R.Context(), &pb.CheckOnWhatsAppRequest{Account: acc, Jids: []*pb.Jid{jid}})
		if err != nil {
			return err
		}
		out := waha.NumberExistResult{}
		if rs := res.GetResults(); len(rs) > 0 {
			out = waha.NumberExistFromPB(rs[0])
		}
		return c.JSON(http.StatusOK, out)
	}
	s.route(r, "GET", "/api/contacts/check-exists", read, checkNumber)
	s.route(r, "GET", "/api/checkNumberStatus", read, checkNumber)

	// Block and unblock answer an empty 201, as WEBJS does (GOWS answers 501).
	block := func(rpc func(c *Ctx, req *pb.JidRequest) error) func(c *Ctx) error {
		return func(c *Ctx) error {
			var body struct {
				ContactID string `json:"contactId"`
			}
			if err := c.Bind(&body); err != nil {
				return err
			}
			if body.ContactID == "" {
				return ErrBadRequest([]string{"contactId must be a string"})
			}
			acc, jid, err := s.dirAccountJID(c, body.ContactID)
			if err != nil {
				return err
			}
			if err := rpc(c, &pb.JidRequest{Account: acc, Jid: jid}); err != nil {
				return err
			}
			return c.Empty(http.StatusCreated)
		}
	}
	send := Route{Action: ActSend, From: FromBody, Working: true}
	s.route(r, "POST", "/api/contacts/block", send, block(func(c *Ctx, req *pb.JidRequest) error {
		_, err := s.Core.Contacts.BlockContact(c.R.Context(), req)
		return err
	}))
	s.route(r, "POST", "/api/contacts/unblock", send, block(func(c *Ctx, req *pb.JidRequest) error {
		_, err := s.Core.Contacts.UnblockContact(c.R.Context(), req)
		return err
	}))

	lids := Route{Action: ActRead, From: FromPath, Working: true}
	s.route(r, "GET", "/api/{session}/lids", lids, func(c *Ctx) error {
		limit, err := dirInt(c, "limit", defaultLidsLimit)
		if err != nil {
			return err
		}
		offset, err := dirInt(c, "offset", 0)
		if err != nil {
			return err
		}
		pairs, err := s.lidPairs(c)
		if err != nil {
			return err
		}
		out := []waha.LidToPhoneNumber{}
		for _, i := range (dirPage{Limit: limit, Offset: offset}).apply(len(pairs)) {
			out = append(out, pairs[i])
		}
		return c.JSON(http.StatusOK, out)
	})
	s.route(r, "GET", "/api/{session}/lids/count", lids, func(c *Ctx) error {
		pairs, err := s.lidPairs(c)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]int{"count": len(pairs)})
	})
	s.route(r, "GET", "/api/{session}/lids/{lid}", lids, func(c *Ctx) error {
		lid := c.Param("lid")
		if !strings.Contains(lid, "@") {
			lid += "@lid"
		}
		if !waha.IsLIDChatID(lid) || strings.HasPrefix(lid, "@") {
			return ErrUnprocessable("Invalid LID - it must end with @lid")
		}
		m, err := s.resolveLid(c, lid)
		if err != nil {
			return err
		}
		out := waha.LidToPhoneNumber{LID: &lid}
		if pn := waha.FromPB(m.GetPn()); pn != "" {
			out.PN = &pn
		}
		return c.JSON(http.StatusOK, out)
	})
	s.route(r, "GET", "/api/{session}/lids/pn/{phoneNumber}", lids, func(c *Ctx) error {
		id := c.Param("phoneNumber")
		if waha.IsLIDChatID(id) {
			return c.JSON(http.StatusOK, waha.LidToPhoneNumber{LID: &id})
		}
		jid, err := dirJID(id)
		if err != nil {
			return err
		}
		pn := waha.FromPB(jid)
		m, err := s.resolveLid(c, jid.GetValue())
		if err != nil {
			return err
		}
		out := waha.LidToPhoneNumber{PN: &pn}
		if lid := waha.FromPB(m.GetLid()); lid != "" {
			out.LID = &lid
		}
		return c.JSON(http.StatusOK, out)
	})
}

// contactInfo answers one contact from the store; an unknown one is still an
// answer, with null names, as WAHA's "always returns result".
func (s *Server) contactInfo(c *Ctx, contactID string) error {
	jid, err := dirJID(contactID)
	if err != nil {
		return err
	}
	id := waha.FromPB(jid)
	ct, err := s.Store.GetContact(c.R.Context(), c.Session, id)
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(http.StatusOK, waha.NewContact(id, "", ""))
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, waha.NewContact(id, ct.Name, ct.PushName))
}

// allContacts reads every stored contact of a session.
func (s *Server) allContacts(ctx context.Context, session string) ([]store.Contact, error) {
	var all []store.Contact
	for {
		batch, err := s.Store.ListContacts(ctx, session, contactsPage, len(all))
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < contactsPage {
			return all, nil
		}
	}
}

// lidPairs is every LID mapping the core has persisted, sorted by lid as
// WAHA sorts them. A mapping missing either side is left out: whatsmeow's
// table, which GOWS lists, only holds whole pairs.
func (s *Server) lidPairs(c *Ctx) ([]waha.LidToPhoneNumber, error) {
	acc, err := c.Account()
	if err != nil {
		return nil, err
	}
	res, err := s.Core.Contacts.ListLidMappings(c.R.Context(), acc)
	if err != nil {
		return nil, err
	}
	out := []waha.LidToPhoneNumber{}
	for _, m := range res.GetMappings() {
		if p := waha.LidPairFromPB(m); p.LID != nil && p.PN != nil {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return *out[i].LID < *out[j].LID })
	return out, nil
}

// resolveLid asks the core for the other side of one jid; nil when unknown.
func (s *Server) resolveLid(c *Ctx, jid string) (*pb.LidPnMapping, error) {
	acc, err := c.Account()
	if err != nil {
		return nil, err
	}
	res, err := s.Core.Contacts.ResolveLidPn(c.R.Context(), &pb.ResolveLidPnRequest{Account: acc, Jids: []*pb.Jid{{Value: jid}}})
	if err != nil {
		return nil, err
	}
	for _, r := range res.GetResults() {
		if r.GetFound() {
			return r.GetMapping(), nil
		}
	}
	return nil, nil
}

// dirAccountJID resolves the route's account and a chat id from the request.
func (s *Server) dirAccountJID(c *Ctx, chatID string) (*pb.AccountRef, *pb.Jid, error) {
	jid, err := dirJID(chatID)
	if err != nil {
		return nil, nil, err
	}
	acc, err := c.Account()
	return acc, jid, err
}

// dirPictureURL is a jid's profile picture, nil when there is none or privacy
// hides it (WAHA answers null then).
func (s *Server) dirPictureURL(ctx context.Context, acc *pb.AccountRef, jid *pb.Jid) (*string, error) {
	res, err := s.Core.Contacts.GetProfilePicture(ctx, &pb.JidRequest{Account: acc, Jid: jid})
	if dirHidden(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if res.GetUrl() == "" {
		return nil, nil
	}
	url := res.GetUrl()
	return &url, nil
}

// --- helpers shared by the contacts, groups, profile and channels routes ---

// dirHidden reports a core error that means "nothing to show": the server
// has no such thing, or privacy settings refuse it.
func dirHidden(err error) bool {
	switch core.CodeOf(err) {
	case codes.NotFound, codes.PermissionDenied:
		return true
	}
	return false
}

// dirJID turns a chat id from a request into the core's jid, or a 400.
func dirJID(chatID string) (*pb.Jid, error) {
	jid, err := waha.ToPB(chatID)
	if err != nil {
		return nil, ErrBadRequest("Invalid chat id: '" + chatID + "'")
	}
	return jid, nil
}

// dirPage is WAHA's in-memory limit/offset paging (PaginatorInMemory), with
// the sort the caller applies.
type dirPage struct {
	Limit, Offset int
	SortBy        string
	Desc          bool
}

// apply returns the indexes of the page within n items. No limit means
// everything from the offset, as WAHA's paginator does.
func (p dirPage) apply(n int) []int {
	start := min(max(p.Offset, 0), n)
	end := n
	if p.Limit > 0 {
		end = min(start+p.Limit, n)
	}
	idx := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		idx = append(idx, i)
	}
	return idx
}

// dirPagination reads WAHA's PaginationParams, with sortBy limited to
// fields, and answers class-validator's 400 for bad values.
func dirPagination(c *Ctx, fields ...string) (dirPage, error) {
	var p dirPage
	var msgs []string
	var err error
	if p.Limit, err = dirInt(c, "limit", 0); err != nil {
		msgs = append(msgs, numberMessage("limit"))
	}
	if p.Offset, err = dirInt(c, "offset", 0); err != nil {
		msgs = append(msgs, numberMessage("offset"))
	}
	if v := c.Query("sortBy"); v != "" {
		known := false
		for _, f := range fields {
			known = known || f == v
		}
		if !known {
			msgs = append(msgs, "sortBy must be one of the following values: "+strings.Join(fields, ", "))
		}
		p.SortBy = v
	}
	switch c.Query("sortOrder") {
	case "", "asc":
	case "desc":
		p.Desc = true
	default:
		msgs = append(msgs, "sortOrder must be one of the following values: desc, asc")
	}
	if len(msgs) > 0 {
		return p, ErrBadRequest(msgs)
	}
	return p, nil
}

func numberMessage(name string) string {
	return name + " must be a number conforming to the specified constraints"
}

// dirInt reads a numeric query parameter, def when absent.
func dirInt(c *Ctx, name string, def int) (int, error) {
	v := c.Query(name)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, ErrBadRequest([]string{numberMessage(name)})
	}
	return int(f), nil
}

// dirBool checks a boolean query parameter the way WAHA's BooleanString
// transform accepts it.
func dirBool(c *Ctx, name string) error {
	switch strings.ToLower(c.Query(name)) {
	case "", "true", "false", "1", "0":
		return nil
	}
	return ErrBadRequest([]string{name + " must be a boolean value"})
}
