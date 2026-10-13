// Package dashboard serves a status page for the local system: health of
// each HTTP service, the NATS server and its clients, and a live feed of
// events. It polls from the server side, so the page needs no CORS setup.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Target is an HTTP service exposing /healthz/live and /healthz/ready.
type Target struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Status struct {
	CheckedAt   time.Time       `json:"checked_at"`
	Services    []ServiceStatus `json:"services"`
	NATS        NATSStatus      `json:"nats"`
	Events      []Event         `json:"events"`
	EventsTotal int64           `json:"events_total"`
}

type ServiceStatus struct {
	Target
	Live      bool     `json:"live"`
	Ready     bool     `json:"ready"`
	Failing   []string `json:"failing,omitempty"`
	LatencyMS int64    `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
}

type NATSStatus struct {
	Available bool         `json:"available"`
	Error     string       `json:"error,omitempty"`
	Version   string       `json:"version,omitempty"`
	Uptime    string       `json:"uptime,omitempty"`
	InMsgs    int64        `json:"in_msgs"`
	OutMsgs   int64        `json:"out_msgs"`
	InBytes   int64        `json:"in_bytes"`
	OutBytes  int64        `json:"out_bytes"`
	Clients   []NATSClient `json:"clients"`
}

type NATSClient struct {
	Name          string         `json:"name"`
	Lang          string         `json:"lang"`
	Addr          string         `json:"addr"`
	Uptime        string         `json:"uptime"`
	RTT           string         `json:"rtt"`
	InMsgs        int64          `json:"in_msgs"`
	OutMsgs       int64          `json:"out_msgs"`
	Subscriptions []Subscription `json:"subscriptions"`
}

type Subscription struct {
	Subject    string `json:"subject"`
	QueueGroup string `json:"queue_group,omitempty"`
	Msgs       int64  `json:"msgs"`
}

// Collector gathers a Status snapshot.
type Collector struct {
	services       []Target
	natsMonitorURL string
	events         *EventLog
	client         *http.Client
}

// NewCollector polls services and the NATS monitoring endpoint at
// natsMonitorURL (nats-server -m 8222); an empty URL skips NATS.
func NewCollector(services []Target, natsMonitorURL string, events *EventLog) *Collector {
	return &Collector{
		services:       services,
		natsMonitorURL: strings.TrimSuffix(natsMonitorURL, "/"),
		events:         events,
		client:         &http.Client{Timeout: 2 * time.Second},
	}
}

// Collect probes everything concurrently, so one slow service does not
// delay the rest.
func (c *Collector) Collect(ctx context.Context) Status {
	st := Status{CheckedAt: time.Now().UTC(), Services: make([]ServiceStatus, len(c.services))}

	var wg sync.WaitGroup
	for i, t := range c.services {
		wg.Go(func() { st.Services[i] = c.probeService(ctx, t) })
	}
	wg.Go(func() { st.NATS = c.probeNATS(ctx) })
	wg.Wait()

	st.Events, st.EventsTotal = c.events.Recent()
	return st
}

func (c *Collector) probeService(ctx context.Context, t Target) ServiceStatus {
	s := ServiceStatus{Target: t}

	start := time.Now()
	code, err := c.getJSON(ctx, t.URL+"/healthz/live", nil)
	s.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Live = code == http.StatusOK

	var ready struct {
		Failing []string `json:"failing"`
	}
	code, err = c.getJSON(ctx, t.URL+"/healthz/ready", &ready)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Ready = code == http.StatusOK
	s.Failing = ready.Failing
	return s
}

func (c *Collector) probeNATS(ctx context.Context) NATSStatus {
	if c.natsMonitorURL == "" {
		return NATSStatus{Error: "NATS monitoring URL not configured"}
	}

	var varz struct {
		Version  string `json:"version"`
		Uptime   string `json:"uptime"`
		InMsgs   int64  `json:"in_msgs"`
		OutMsgs  int64  `json:"out_msgs"`
		InBytes  int64  `json:"in_bytes"`
		OutBytes int64  `json:"out_bytes"`
	}
	if _, err := c.getJSON(ctx, c.natsMonitorURL+"/varz", &varz); err != nil {
		return NATSStatus{Error: "monitoring endpoint unreachable (start nats-server with -m 8222): " + err.Error()}
	}

	var connz struct {
		Connections []struct {
			Name    string `json:"name"`
			Lang    string `json:"lang"`
			IP      string `json:"ip"`
			Port    int    `json:"port"`
			Uptime  string `json:"uptime"`
			RTT     string `json:"rtt"`
			InMsgs  int64  `json:"in_msgs"`
			OutMsgs int64  `json:"out_msgs"`
			Subs    []struct {
				Subject string `json:"subject"`
				QGroup  string `json:"qgroup"`
				Msgs    int64  `json:"msgs"`
			} `json:"subscriptions_list_detail"`
		} `json:"connections"`
	}
	if _, err := c.getJSON(ctx, c.natsMonitorURL+"/connz?subs=detail", &connz); err != nil {
		return NATSStatus{Error: "read connections: " + err.Error()}
	}

	st := NATSStatus{
		Available: true,
		Version:   varz.Version,
		Uptime:    varz.Uptime,
		InMsgs:    varz.InMsgs,
		OutMsgs:   varz.OutMsgs,
		InBytes:   varz.InBytes,
		OutBytes:  varz.OutBytes,
		Clients:   make([]NATSClient, 0, len(connz.Connections)),
	}
	for _, conn := range connz.Connections {
		cl := NATSClient{
			Name:    conn.Name,
			Lang:    conn.Lang,
			Addr:    fmt.Sprintf("%s:%d", conn.IP, conn.Port),
			Uptime:  conn.Uptime,
			RTT:     conn.RTT,
			InMsgs:  conn.InMsgs,
			OutMsgs: conn.OutMsgs,
		}
		for _, sub := range conn.Subs {
			cl.Subscriptions = append(cl.Subscriptions, Subscription{Subject: sub.Subject, QueueGroup: sub.QGroup, Msgs: sub.Msgs})
		}
		st.Clients = append(st.Clients, cl)
	}
	return st
}

// getJSON GETs url and, if v is non-nil, decodes the body into it. Any
// HTTP status is returned without error: a 503 from a readiness probe is
// information, not a failure to probe.
func (c *Collector) getJSON(ctx context.Context, url string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}
