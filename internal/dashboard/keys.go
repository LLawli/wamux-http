package dashboard

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"github.com/go-chi/chi/v5"
)

// keysPage is the keys view: the table and the side form.
type keysPage struct {
	Keys     []key
	Sessions []string
	Form     keyForm
	Created  *key // the key just created, shown once
}

// keyForm is the new-key form, kept when creating fails.
type keyForm struct {
	Admin    bool
	Sessions map[string]bool
	Actions  map[string]bool
}

// SessionList is every session and whether the form picked it.
func (f keyForm) SessionList(all []string) []grant {
	var out []grant
	for _, s := range all {
		out = append(out, grant{Name: s, Label: s, On: f.Sessions[s]})
	}
	return out
}

// ActionList is every action and whether the form grants it.
func (f keyForm) ActionList() []grant {
	var out []grant
	for _, a := range keyActions {
		out = append(out, grant{Name: a.Name, Label: a.Label, On: f.Actions[a.Name]})
	}
	return out
}

func newKeyForm() keyForm {
	acts := map[string]bool{}
	for k, v := range defaultActions {
		acts[k] = v
	}
	return keyForm{Sessions: map[string]bool{}, Actions: acts}
}

func (h *Handler) keysPage(ctx context.Context, a api) (keysPage, error) {
	kp := keysPage{Form: newKeyForm()}
	keys, err := a.keys(ctx)
	if err != nil {
		return kp, err
	}
	kp.Keys = keys
	list, err := a.sessions(ctx)
	if err != nil {
		return kp, err
	}
	for _, s := range list {
		kp.Sessions = append(kp.Sessions, s.Name)
	}
	return kp, nil
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	f := keyForm{Admin: r.PostFormValue("scope") == "admin", Sessions: map[string]bool{}, Actions: map[string]bool{}}
	for _, name := range r.PostForm["s"] {
		f.Sessions[name] = true
	}
	for _, act := range r.PostForm["a"] {
		f.Actions[act] = true
	}
	body := map[string]any{"isAdmin": f.Admin, "isActive": true}
	if !f.Admin {
		sessions := []string{}
		for name := range f.Sessions {
			sessions = append(sessions, name)
		}
		slices.Sort(sessions)
		actions := map[string]bool{}
		for _, act := range keyActions {
			actions[act.Name] = f.Actions[act.Name]
		}
		body["sessions"], body["actions"] = sessions, actions
	}
	res := a.do(ctx, "POST", "/api/keys", body)
	kp, err := h.keysPage(ctx, a)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	if !res.ok() {
		kp.Form = f
		h.toast(s, "bad", res.message())
		_ = s.patchElements(h.render("key-side", kp), patchOpts{})
		return
	}
	var created key
	_ = res.decode(&created)
	kp.Created = &created
	_ = s.patchElements(h.render("key-table", kp), patchOpts{})
	_ = s.patchElements(h.render("key-side", kp), patchOpts{})
}

func (h *Handler) toggleKey(w http.ResponseWriter, r *http.Request) {
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	keys, err := a.keys(ctx)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	for _, k := range keys {
		if k.ID != id {
			continue
		}
		body := map[string]any{"isAdmin": k.IsAdmin, "isActive": !k.IsActive, "sessions": k.Sessions}
		if k.Actions != nil {
			body["actions"] = k.Actions
		}
		if res := a.do(ctx, "PUT", "/api/keys/"+url.PathEscape(id), body); !res.ok() {
			h.toast(s, "bad", res.message())
		}
	}
	h.keyTable(ctx, s, a)
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	s, a, ok := h.begin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if res := a.do(ctx, "DELETE", "/api/keys/"+url.PathEscape(chi.URLParam(r, "id")), nil); !res.ok() {
		h.toast(s, "bad", res.message())
	}
	h.keyTable(ctx, s, a)
}

func (h *Handler) keyTable(ctx context.Context, s *sse, a api) {
	kp, err := h.keysPage(ctx, a)
	if err != nil {
		h.toast(s, "bad", err.Error())
		return
	}
	_ = s.patchElements(h.render("key-table", kp), patchOpts{})
}
