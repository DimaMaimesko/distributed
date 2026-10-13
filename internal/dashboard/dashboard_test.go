package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"distributed/platform/middleware"
)

// fakeService answers health probes like the real services do.
func fakeService(t *testing.T, ready bool, failing ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz/live", middleware.LivenessHandler)
	mux.HandleFunc("GET /healthz/ready", func(w http.ResponseWriter, r *http.Request) {
		if ready {
			middleware.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		middleware.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "failing": failing})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeNATSMonitor serves the parts of nats-server's monitoring API we read,
// with field names as nats-server 2.x returns them.
func fakeNATSMonitor(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /varz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"version":"2.15.0","uptime":"1m","in_msgs":10,"out_msgs":20,"in_bytes":100,"out_bytes":200}`)
	})
	mux.HandleFunc("GET /connz", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("subs") != "detail" {
			t.Errorf("connz queried without subs=detail: %s", r.URL)
		}
		io.WriteString(w, `{"connections":[
			{"name":"orders","lang":"go","ip":"127.0.0.1","port":5001,"uptime":"1m","rtt":"1ms","in_msgs":5,"out_msgs":0},
			{"name":"consumer","lang":"go","ip":"127.0.0.1","port":5002,"uptime":"1m","rtt":"2ms","in_msgs":0,"out_msgs":5,
			 "subscriptions_list_detail":[{"subject":"orders.created","qgroup":"workers","msgs":5}]}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCollect(t *testing.T) {
	auth := fakeService(t, true)
	orders := fakeService(t, false, "nats")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // connection refused

	events := NewEventLog(10)
	events.Add("orders.created", []byte(`{"id":"ord_1"}`))

	c := NewCollector([]Target{
		{Name: "auth", URL: auth.URL},
		{Name: "orders", URL: orders.URL},
		{Name: "billing", URL: down.URL},
	}, fakeNATSMonitor(t).URL, events)
	st := c.Collect(context.Background())

	got := map[string]ServiceStatus{}
	for _, s := range st.Services {
		got[s.Name] = s
	}
	if s := got["auth"]; !s.Live || !s.Ready {
		t.Errorf("auth = %+v, want live and ready", s)
	}
	if s := got["orders"]; !s.Live || s.Ready || !slices.Equal(s.Failing, []string{"nats"}) {
		t.Errorf("orders = %+v, want live, not ready, failing nats", s)
	}
	if s := got["billing"]; s.Live || s.Error == "" {
		t.Errorf("billing = %+v, want down with an error", s)
	}

	n := st.NATS
	if !n.Available || n.Version != "2.15.0" || n.InMsgs != 10 || len(n.Clients) != 2 {
		t.Fatalf("nats = %+v", n)
	}
	sub := n.Clients[1].Subscriptions
	if n.Clients[1].Name != "consumer" || len(sub) != 1 || sub[0].QueueGroup != "workers" || sub[0].Msgs != 5 {
		t.Errorf("consumer client = %+v", n.Clients[1])
	}

	if st.EventsTotal != 1 || len(st.Events) != 1 {
		t.Errorf("events = %d/%d", len(st.Events), st.EventsTotal)
	}
}

func TestCollect_NATSMonitoringDown(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	st := NewCollector(nil, down.URL, NewEventLog(1)).Collect(context.Background())
	if st.NATS.Available || !strings.Contains(st.NATS.Error, "-m 8222") {
		t.Errorf("nats = %+v, want unavailable with a hint", st.NATS)
	}
}

func TestEventLog_KeepsNewestFirst(t *testing.T) {
	l := NewEventLog(3)
	for i := 1; i <= 5; i++ {
		l.Add("orders.created", []byte(fmt.Sprintf(`{"n":%d}`, i)))
	}
	l.Add("orders.created", []byte("not json"))

	events, total := l.Recent()
	if total != 6 || len(events) != 3 {
		t.Fatalf("total=%d len=%d", total, len(events))
	}
	if events[0].Data != nil {
		t.Errorf("invalid JSON kept: %s", events[0].Data)
	}
	var want = []string{`{"n":5}`, `{"n":4}`}
	for i, w := range want {
		if string(events[i+1].Data) != w {
			t.Errorf("events[%d] = %s, want %s", i+1, events[i+1].Data, w)
		}
	}
}

func TestServer_ServesPageAndStatus(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{}, NewCollector(nil, "", NewEventLog(1))).Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>System Status</title>") {
		t.Errorf("GET / = %d, page missing", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET /api/status = %d, %v", resp.StatusCode, err)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
}
