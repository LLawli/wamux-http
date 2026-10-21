package dashboard_test

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"testing"

	pb "github.com/LLawli/wamux-http/internal/core/wamuxv1"
	"github.com/LLawli/wamux-http/internal/fakecore"
	"github.com/LLawli/wamux-http/internal/testenv"
)

// TestDevServer runs the panel on the fake core, to look at it in a
// browser. It is skipped unless DASHBOARD_DEV names an address for the
// phone controls; the edge's own URL is printed:
//
//	DASHBOARD_DEV=127.0.0.1:3199 go test ./internal/dashboard -run TestDevServer -v -timeout 0
//
// The controls play the phone: GET /scan/{session} pairs the session,
// GET /logout/{session} logs it out from the phone.
func TestDevServer(t *testing.T) {
	addr := os.Getenv("DASHBOARD_DEV")
	if addr == "" {
		t.Skip("set DASHBOARD_DEV to run the panel")
	}
	e := testenv.Start(t, testenv.Options{Core: fakecore.Options{Contacts: fakecore.NewContacts("Loja Centro")}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scan/{name}", func(w http.ResponseWriter, r *http.Request) {
		err := e.Core.ScanQR(r.PathValue("name"), "556181230101@s.whatsapp.net", "Loja Centro")
		_, _ = fmt.Fprintln(w, err)
	})
	mux.HandleFunc("GET /logout/{name}", func(w http.ResponseWriter, r *http.Request) {
		e.Core.Disconnect(r.PathValue("name"), pb.ConnectionState_LOGGED_OUT)
		_, _ = fmt.Fprintln(w, "ok")
	})
	go func() { _ = http.ListenAndServe(addr, mux) }()
	fmt.Printf("panel: %s/dashboard  admin key: %s  phone: http://%s\n", e.URL, testenv.AdminKey, addr)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
