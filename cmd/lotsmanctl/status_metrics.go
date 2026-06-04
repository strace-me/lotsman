package main

import (
	"strconv"
	"strings"
)

// serviceStatus is the parsed, per-service view assembled from the daemon's
// Prometheus /metrics text. Fields default to their zero value when the
// corresponding gauge is absent for a service.
type serviceStatus struct {
	Service       string
	State         string // from lotsman_service_position{state=...}
	Position      int
	Broken        bool
	Fails         int
	LeakRatio     float64
	DeadFlowRatio float64
	Flows         int
	Misrouted     bool   // any lotsman_service_misrouted{kind=...} == 1
	MisrouteKind  string // first kind that flagged (for display)
}

// parseMetrics is a pure, tiny line parser for the subset of lotsman gauges the
// status table needs. It deliberately avoids a Prometheus client: each sample
// line is `name{label="v",...} value`, so we split on '{' and '}' and read the
// trailing value. Comment (#) and unrelated lines are ignored. The returned map
// is keyed by service name.
func parseMetrics(text string) map[string]*serviceStatus {
	out := map[string]*serviceStatus{}
	get := func(svc string) *serviceStatus {
		s := out[svc]
		if s == nil {
			s = &serviceStatus{Service: svc}
			out[svc] = s
		}
		return s
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ok := splitSample(line)
		if !ok {
			continue
		}
		svc := labels["service"]
		if svc == "" {
			continue
		}
		switch name {
		case "lotsman_service_position":
			s := get(svc)
			s.State = labels["state"]
			s.Position = atoiSafe(value)
		case "lotsman_service_broken":
			get(svc).Broken = value != "0"
		case "lotsman_active_fails":
			get(svc).Fails = atoiSafe(value)
		case "lotsman_service_leak_ratio":
			get(svc).LeakRatio = atofSafe(value)
		case "lotsman_service_dead_flow_ratio":
			get(svc).DeadFlowRatio = atofSafe(value)
		case "lotsman_service_flows":
			get(svc).Flows = atoiSafe(value)
		case "lotsman_service_misrouted":
			if value != "0" {
				s := get(svc)
				s.Misrouted = true
				if s.MisrouteKind == "" {
					s.MisrouteKind = labels["kind"]
				}
			} else {
				// Touch the service so a service that only appears in observe
				// metrics is still listed; harmless if already present.
				get(svc)
			}
		}
	}
	return out
}

// splitSample parses one Prometheus sample line into (metricName, labels, value).
// Returns ok=false for lines that don't look like a sample.
func splitSample(line string) (name string, labels map[string]string, value string, ok bool) {
	labels = map[string]string{}
	if i := strings.IndexByte(line, '{'); i >= 0 {
		j := strings.LastIndexByte(line, '}')
		if j < i {
			return "", nil, "", false
		}
		name = line[:i]
		parseLabels(line[i+1:j], labels)
		rest := strings.TrimSpace(line[j+1:])
		if rest == "" {
			return "", nil, "", false
		}
		value = firstField(rest)
		return name, labels, value, true
	}
	// Unlabeled sample: `name value`. Not used by status (all our gauges are
	// labelled by service) but handled for completeness.
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", nil, "", false
	}
	return fields[0], labels, fields[1], true
}

// parseLabels reads `k="v",k2="v2"` into m. Values are always quoted in the
// daemon's output (fmt %q), so we strip the surrounding quotes.
func parseLabels(s string, m map[string]string) {
	for _, pair := range splitLabelPairs(s) {
		k, v, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"`)
		m[k] = v
	}
}

// splitLabelPairs splits the label block on commas that are outside quotes, so a
// comma inside a label value (rare, but possible) does not break a pair.
func splitLabelPairs(s string) []string {
	var pairs []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case ',':
			if inQuote {
				cur.WriteByte(c)
			} else {
				pairs = append(pairs, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		pairs = append(pairs, cur.String())
	}
	return pairs
}

func firstField(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func atoiSafe(s string) int {
	// Prometheus may render an int gauge as a float ("3" or "3.0"); take the
	// integer part.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f)
	}
	return 0
}

func atofSafe(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
