package workers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TailProtocol is the tail WebSocket subprotocol.
const TailProtocol = "trace-v1"

// TailFilterOptions are the tail filters wrangler exposes.
type TailFilterOptions struct {
	Status   []string // ok, error, canceled
	Methods  []string
	Headers  []string // "name" or "name:value"
	ClientIP []string
	Search   string
	Sampling float64 // 0 < rate <= 1; 0 = unset
}

// Filters builds the filter list sent to the tail API.
func (o TailFilterOptions) Filters() ([]map[string]any, error) {
	var out []map[string]any
	if o.Sampling != 0 {
		if o.Sampling < 0 || o.Sampling > 1 {
			return nil, fmt.Errorf("--sampling-rate must be between 0 and 1")
		}
		out = append(out, map[string]any{"sampling_rate": o.Sampling})
	}
	if len(o.Status) > 0 {
		var outcomes []string
		for _, s := range o.Status {
			switch strings.ToLower(s) {
			case "ok":
				outcomes = append(outcomes, "ok")
			case "error":
				outcomes = append(outcomes, "exception", "exceededCpu", "exceededMemory", "unknown")
			case "canceled", "cancelled":
				outcomes = append(outcomes, "canceled")
			default:
				return nil, fmt.Errorf("--status %q: want ok, error, or canceled", s)
			}
		}
		out = append(out, map[string]any{"outcome": outcomes})
	}
	if len(o.Methods) > 0 {
		ms := make([]string, len(o.Methods))
		for i, m := range o.Methods {
			ms[i] = strings.ToUpper(m)
		}
		out = append(out, map[string]any{"method": ms})
	}
	for _, h := range o.Headers {
		k, v, hasV := strings.Cut(h, ":")
		f := map[string]any{"key": strings.TrimSpace(k)}
		if hasV {
			f["query"] = strings.TrimSpace(v)
		}
		out = append(out, map[string]any{"header": f})
	}
	if len(o.ClientIP) > 0 {
		out = append(out, map[string]any{"client_ip": o.ClientIP})
	}
	if o.Search != "" {
		out = append(out, map[string]any{"query": o.Search})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

// TailEvent is one trace-v1 message (only the fields cfctl prints).
type TailEvent struct {
	Outcome        string `json:"outcome"`
	ScriptName     string `json:"scriptName"`
	EventTimestamp int64  `json:"eventTimestamp"`
	Exceptions     []struct {
		Name      string `json:"name"`
		Message   string `json:"message"`
		Timestamp int64  `json:"timestamp"`
	} `json:"exceptions"`
	Logs []struct {
		Message   []any  `json:"message"`
		Level     string `json:"level"`
		Timestamp int64  `json:"timestamp"`
	} `json:"logs"`
	Event map[string]any `json:"event"`
}

var outcomeText = map[string]string{
	"ok": "Ok", "canceled": "Canceled", "exception": "Exception", "exceededCpu": "Exceeded CPU Limit",
	"exceededMemory": "Exceeded Memory Limit", "unknown": "Unknown", "scriptNotFound": "Script Not Found",
	"responseStreamDisconnected": "Response Stream Disconnected",
}

// FormatTailPretty renders a tail message like `wrangler tail --format pretty`.
func FormatTailPretty(raw []byte, loc *time.Location) string {
	var e TailEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if loc == nil {
		loc = time.Local
	}
	when := ""
	if e.EventTimestamp > 0 {
		when = time.UnixMilli(e.EventTimestamp).In(loc).Format("2006-01-02 15:04:05")
	}
	outcome := outcomeText[e.Outcome]
	if outcome == "" {
		outcome = e.Outcome
	}
	var head string
	ev := e.Event
	switch {
	case ev == nil:
		head = fmt.Sprintf("Unknown Event - %s @ %s", outcome, when)
	case ev["request"] != nil:
		req, _ := ev["request"].(map[string]any)
		status := ""
		if resp, ok := ev["response"].(map[string]any); ok {
			if s, ok := resp["status"].(float64); ok {
				status = fmt.Sprintf(" %d", int(s))
			}
		}
		head = fmt.Sprintf("%v %v -%s %s @ %s", req["method"], req["url"], status, outcome, when)
	case ev["cron"] != nil:
		head = fmt.Sprintf("%q @ %s - %s", ev["cron"], when, outcome)
	case ev["queue"] != nil:
		head = fmt.Sprintf("Queue %v (%v messages) - %s @ %s", ev["queue"], ev["batchSize"], outcome, when)
	case ev["mailFrom"] != nil:
		head = fmt.Sprintf("Email from:%v to:%v size:%v @ %s - %s", ev["mailFrom"], ev["rcptTo"], ev["rawSize"], when, outcome)
	case ev["consumedEvents"] != nil:
		head = fmt.Sprintf("Tail event @ %s - %s", when, outcome)
	case ev["scheduledTime"] != nil:
		head = fmt.Sprintf("Alarm @ %s - %s", when, outcome)
	default:
		head = fmt.Sprintf("Event @ %s - %s", when, outcome)
	}
	var b strings.Builder
	b.WriteString(head)
	for _, l := range e.Logs {
		parts := make([]string, len(l.Message))
		for i, m := range l.Message {
			if s, ok := m.(string); ok {
				parts[i] = s
			} else {
				j, _ := json.Marshal(m)
				parts[i] = string(j)
			}
		}
		fmt.Fprintf(&b, "\n  (%s) %s", l.Level, strings.Join(parts, " "))
	}
	for _, x := range e.Exceptions {
		fmt.Fprintf(&b, "\n  ✘ %s: %s", x.Name, x.Message)
	}
	return b.String()
}
