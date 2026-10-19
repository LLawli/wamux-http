package httpapi_test

import (
	"context"
	"strings"
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/store"
	"github.com/LLawli/wamux-http/internal/testenv"
)

const meJID = "5511999999999:7@s.whatsapp.net"

// directoryEnv is an edge with one WORKING session "default" and the given
// fakes behind it.
func directoryEnv(t *testing.T, opts fakecore.Options) *testenv.Env {
	t.Helper()
	e := testenv.Start(t, testenv.Options{Core: opts})
	e.WorkingSession("default", meJID, nil)
	return e
}

// withDivergence skips the contract check of one route and status while do
// runs: WAHA itself answers null where its OpenAPI says string there.
// Tests in this package do not run in parallel, so the shared map is safe.
func withDivergence(key, why string, do func()) {
	testenv.Divergences[key] = why
	defer delete(testenv.Divergences, key)
	do()
}

func TestContactsFromStore(t *testing.T) {
	e := directoryEnv(t, fakecore.Options{})
	ctx := context.Background()
	for _, c := range []store.Contact{
		{Session: "default", ID: "5511111111111@c.us", Name: "Zeca", PushName: "Z"},
		{Session: "default", ID: "5511222222222@c.us", PushName: "Ana"},
		{Session: "default", ID: "123456789@lid", Name: "Bia"},
		{Session: "other", ID: "5511333333333@c.us", Name: "Not mine"},
	} {
		if err := e.App.Store.UpsertContact(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	r := e.Do("GET", "/api/contacts/all?session=default", nil)
	if r.Status != 200 || string(r.Body) != `[{"id":"123456789@lid","name":"Bia","pushname":null},{"id":"5511111111111@c.us","name":"Zeca","pushname":"Z"},{"id":"5511222222222@c.us","name":null,"pushname":"Ana"}]`+"\n" {
		t.Fatalf("all: %d %s", r.Status, r.Body)
	}
	r = e.Do("GET", "/api/contacts/all?session=default&sortBy=name&sortOrder=desc&limit=2", nil)
	var page []map[string]any
	r.JSON(t, &page)
	if len(page) != 2 || page[0]["name"] != "Zeca" || page[1]["name"] != "Bia" {
		t.Fatalf("sorted by name, desc, limit 2: %s", r.Body)
	}
	r = e.Do("GET", "/api/contacts/all?session=default&offset=2", nil)
	page = nil
	if r.JSON(t, &page); len(page) != 1 || page[0]["id"] != "5511222222222@c.us" {
		t.Fatalf("offset: %s", r.Body)
	}
	if r := e.Do("GET", "/api/contacts/all?session=default&sortBy=age", nil); r.Status != 400 || !strings.Contains(string(r.Body), "sortBy must be one of the following values: id, name") {
		t.Fatalf("bad sortBy: %d %s", r.Status, r.Body)
	}

	// One contact, in any id form, by query or by path.
	for _, path := range []string{
		"/api/contacts?session=default&contactId=5511111111111",
		"/api/contacts?session=default&contactId=5511111111111@s.whatsapp.net",
		"/api/default/contacts/5511111111111@c.us",
	} {
		if r := e.Do("GET", path, nil); r.Status != 200 || string(r.Body) != `{"id":"5511111111111@c.us","name":"Zeca","pushname":"Z"}`+"\n" {
			t.Fatalf("%s: %d %s", path, r.Status, r.Body)
		}
	}
	if r := e.Do("GET", "/api/default/contacts/5511444444444@c.us", nil); r.Status != 200 || string(r.Body) != `{"id":"5511444444444@c.us","name":null,"pushname":null}`+"\n" {
		t.Fatalf("unknown contact still answers: %d %s", r.Status, r.Body)
	}

	// The deprecated routes take the session from the query.
	if r := e.Do("GET", "/api/contacts?contactId=1", nil); r.Status != 400 || r.Map(t)["message"] != "Session name is required" {
		t.Fatalf("no session: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/contacts?session=nope&contactId=1", nil); r.Status != 422 {
		t.Fatalf("unknown session: %d %s", r.Status, r.Body)
	}
}

func TestContactsCore(t *testing.T) {
	contacts := fakecore.NewContacts("Ana")
	contacts.Registered["5511777777777@s.whatsapp.net"] = "5511777777777@s.whatsapp.net"
	contacts.Pictures["5511777777777@s.whatsapp.net"] = "https://pps.whatsapp.net/pic.jpg"
	contacts.Abouts["5511777777777@s.whatsapp.net"] = "Busy"
	e := directoryEnv(t, fakecore.Options{Contacts: contacts})

	r := e.Do("GET", "/api/contacts/check-exists?session=default&phone=%2B5511777777777", nil)
	if r.Status != 200 || string(r.Body) != `{"numberExists":true,"chatId":"5511777777777@c.us","pn":"5511777777777@c.us"}`+"\n" {
		t.Fatalf("check-exists: %d %s", r.Status, r.Body)
	}
	checks := fakecore.Logged[*pb.CheckOnWhatsAppRequest](&contacts.RequestLog)
	if len(checks) != 1 || checks[0].GetJids()[0].GetValue() != "5511777777777@s.whatsapp.net" || checks[0].GetAccount().GetUuid() == "" {
		t.Fatalf("core got %v", checks)
	}
	if r := e.Do("GET", "/api/checkNumberStatus?session=default&phone=5511000000000", nil); r.Status != 200 || string(r.Body) != `{"numberExists":false}`+"\n" {
		t.Fatalf("checkNumberStatus: %d %s", r.Status, r.Body)
	}

	if r := e.Do("GET", "/api/contacts/profile-picture?session=default&contactId=5511777777777@c.us&refresh=true", nil); r.Status != 200 || r.Map(t)["profilePictureURL"] != "https://pps.whatsapp.net/pic.jpg" {
		t.Fatalf("picture: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/contacts/profile-picture?session=default&contactId=5511000000000@c.us", nil); r.Status != 200 || string(r.Body) != `{"profilePictureURL":null}`+"\n" {
		t.Fatalf("no picture is null: %d %s", r.Status, r.Body)
	}

	if r := e.Do("GET", "/api/contacts/about?session=default&contactId=5511777777777@c.us", nil); r.Status != 200 || r.Map(t)["about"] != "Busy" {
		t.Fatalf("about: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/contacts/about?session=default&contactId=5511000000000@c.us", nil); r.Status != 200 || len(r.Body) != 0 {
		t.Fatalf("hidden about is an empty answer: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/contacts/about?session=default&contactId=not a number", nil); r.Status != 400 {
		t.Fatalf("bad id: %d %s", r.Status, r.Body)
	}
}

func TestLids(t *testing.T) {
	contacts := fakecore.NewContacts("Ana")
	contacts.Lids = []*pb.LidPnMapping{
		{Lid: &pb.Jid{Value: "300@lid"}, Pn: &pb.Jid{Value: "5511300000000@s.whatsapp.net"}},
		{Lid: &pb.Jid{Value: "100@lid"}, Pn: &pb.Jid{Value: "5511100000000@s.whatsapp.net"}},
		{Lid: &pb.Jid{Value: "200@lid"}},
		{Pn: &pb.Jid{Value: "5511400000000@s.whatsapp.net"}},
	}
	e := directoryEnv(t, fakecore.Options{Contacts: contacts})

	if r := e.Do("GET", "/api/default/lids/count", nil); r.Status != 200 || string(r.Body) != `{"count":2}`+"\n" {
		t.Fatalf("count: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/lids?limit=1", nil); r.Status != 200 || string(r.Body) != `[{"lid":"100@lid","pn":"5511100000000@c.us"}]`+"\n" {
		t.Fatalf("first page, sorted by lid, whole pairs only: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/lids?offset=1", nil); r.Status != 200 || string(r.Body) != `[{"lid":"300@lid","pn":"5511300000000@c.us"}]`+"\n" {
		t.Fatalf("offset: %d %s", r.Status, r.Body)
	}
	if r := e.Do("GET", "/api/default/lids?limit=x", nil); r.Status != 400 {
		t.Fatalf("bad limit: %d %s", r.Status, r.Body)
	}

	if r := e.Do("GET", "/api/default/lids/100", nil); r.Status != 200 || string(r.Body) != `{"lid":"100@lid","pn":"5511100000000@c.us"}`+"\n" {
		t.Fatalf("lid without suffix: %d %s", r.Status, r.Body)
	}
	got := fakecore.Logged[*pb.ResolveLidPnRequest](&contacts.RequestLog)
	if len(got) != 1 || got[0].GetJids()[0].GetValue() != "100@lid" {
		t.Fatalf("core got %v", got)
	}
	withDivergence("GET /api/{session}/lids/{lid} 200", "WAHA answers pn null for an unknown lid", func() {
		if r := e.Do("GET", "/api/default/lids/999@lid", nil); r.Status != 200 || string(r.Body) != `{"lid":"999@lid","pn":null}`+"\n" {
			t.Fatalf("unknown lid: %d %s", r.Status, r.Body)
		}
	})
	if r := e.Do("GET", "/api/default/lids/5511@c.us", nil); r.Status != 422 || r.Map(t)["message"] != "Invalid LID - it must end with @lid" {
		t.Fatalf("not a lid: %d %s", r.Status, r.Body)
	}

	if r := e.Do("GET", "/api/default/lids/pn/5511300000000", nil); r.Status != 200 || string(r.Body) != `{"lid":"300@lid","pn":"5511300000000@c.us"}`+"\n" {
		t.Fatalf("pn: %d %s", r.Status, r.Body)
	}
	if got := fakecore.Logged[*pb.ResolveLidPnRequest](&contacts.RequestLog); got[len(got)-1].GetJids()[0].GetValue() != "5511300000000@s.whatsapp.net" {
		t.Fatalf("the core gets the phone jid: %v", got[len(got)-1])
	}
	withDivergence("GET /api/{session}/lids/pn/{phoneNumber} 200", "WAHA answers lid null for an unknown phone", func() {
		if r := e.Do("GET", "/api/default/lids/pn/5511000000000@c.us", nil); r.Status != 200 || string(r.Body) != `{"lid":null,"pn":"5511000000000@c.us"}`+"\n" {
			t.Fatalf("unknown pn: %d %s", r.Status, r.Body)
		}
		before := len(fakecore.Logged[*pb.ResolveLidPnRequest](&contacts.RequestLog))
		if r := e.Do("GET", "/api/default/lids/pn/100@lid", nil); r.Status != 200 || string(r.Body) != `{"lid":"100@lid","pn":null}`+"\n" {
			t.Fatalf("a lid passes through: %d %s", r.Status, r.Body)
		}
		if after := len(fakecore.Logged[*pb.ResolveLidPnRequest](&contacts.RequestLog)); after != before {
			t.Fatal("a lid must not reach the core")
		}
	})
}

func TestDirectoryPolicies(t *testing.T) {
	e := directoryEnv(t, fakecore.Options{Contacts: fakecore.NewContacts("Ana"), Groups: fakecore.NewGroups()})
	r := e.Do("POST", "/api/keys", map[string]any{"isAdmin": false, "session": "default", "actions": map[string]bool{"read": true}})
	if r.Status != 201 {
		t.Fatalf("key: %d %s", r.Status, r.Body)
	}
	key := r.Map(t)["key"].(string)
	if r := e.DoKey(key, "GET", "/api/default/groups/count", nil); r.Status != 200 {
		t.Fatalf("read with a read key: %d %s", r.Status, r.Body)
	}
	if r := e.DoKey(key, "GET", "/api/contacts/all?session=default", nil); r.Status != 200 {
		t.Fatalf("contacts read with a read key: %d %s", r.Status, r.Body)
	}
	for _, w := range []struct{ method, path string }{
		{"POST", "/api/default/groups"},
		{"PUT", "/api/default/profile/name"},
		{"PUT", "/api/default/groups/1@g.us/subject"},
	} {
		if r := e.DoKey(key, w.method, w.path, map[string]any{}); r.Status != 403 {
			t.Fatalf("%s %s needs send: %d %s", w.method, w.path, r.Status, r.Body)
		}
	}
	if r := e.DoKey(key, "GET", "/api/contacts/all?session=other", nil); r.Status != 403 {
		t.Fatalf("another session: %d %s", r.Status, r.Body)
	}
}

func TestDirectoryNotImplemented(t *testing.T) {
	e := directoryEnv(t, fakecore.Options{})
	for _, w := range []struct{ method, path string }{
		{"POST", "/api/contacts/block"},
		{"PUT", "/api/default/contacts/5511@c.us"},
		{"DELETE", "/api/default/groups/1@g.us"},
		{"PUT", "/api/default/groups/1@g.us/settings/security/membership-approval"},
		{"PUT", "/api/default/groups/1@g.us/settings/security/member-add-mode"},
		{"PUT", "/api/default/profile/status"},
		{"POST", "/api/default/channels"},
		{"POST", "/api/default/channels/1@newsletter/follow"},
		{"POST", "/api/default/calls/reject"},
	} {
		if r := e.Do(w.method, w.path, map[string]any{}); r.Status != 501 {
			t.Fatalf("%s %s: %d %s", w.method, w.path, r.Status, r.Body)
		}
	}
}
