package protocol

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Device queries are the agent's built-in, read-only reports (job type
// device_query, docs/23-ai-autonomy.md). Each runs fixed code in the agent: the
// request carries only a query name and parameters from closed sets, checked
// here by both the server and the agent, so no code and no free text ever
// reaches a command line.
const (
	DeviceQueryProcesses = "processes" // top processes by CPU or memory
	DeviceQueryServices  = "services"  // service state, optionally filtered by name
	DeviceQueryEvents    = "events"    // recent errors from the event log or journal
	DeviceQueryDisk      = "disk"      // usage per volume
	DeviceQueryNetwork   = "network"   // interfaces, listening ports, connection summary
	DeviceQuerySystem    = "system"    // uptime, pending reboot, OS
	DeviceQuerySoftware  = "software"  // installed software, read live
)

// JobTypeDeviceQuery is the job type that runs a device query
// (models.JobDeviceQuery on the server).
const JobTypeDeviceQuery = "device_query"

// DeviceQueryRequest is the Script of a device_query job, as JSON.
type DeviceQueryRequest struct {
	Query  string            `json:"query"`
	Params map[string]string `json:"params,omitempty"`
}

// MaxDeviceQueryOutput bounds what a query returns, before the server bounds
// it again for the model.
const MaxDeviceQueryOutput = 64 << 10

type queryParam struct {
	enum     []string
	min, max int // for integers; both zero = not an integer
	pattern  *regexp.Regexp
	def      string
}

// A name filter matches against names, never a command: letters, digits and a
// few separators service and package names actually use.
var nameFilter = regexp.MustCompile(`^[A-Za-z0-9 ._@:+-]{1,64}$`)

var deviceQueries = map[string]map[string]queryParam{
	DeviceQueryProcesses: {
		"sort":  {enum: []string{"cpu", "memory"}, def: "cpu"},
		"limit": {min: 1, max: 25, def: "15"},
	},
	DeviceQueryServices: {
		"name":  {pattern: nameFilter},
		"state": {enum: []string{"any", "running", "stopped"}, def: "any"},
	},
	DeviceQueryEvents: {
		"hours":  {min: 1, max: 72, def: "24"},
		"source": {pattern: nameFilter},
		"limit":  {min: 1, max: 100, def: "50"},
	},
	DeviceQueryDisk: {},
	DeviceQueryNetwork: {
		"section": {enum: []string{"config", "listening", "connections"}, def: "config"},
	},
	DeviceQuerySystem: {},
	DeviceQuerySoftware: {
		"name":  {pattern: nameFilter},
		"limit": {min: 1, max: 200, def: "100"},
	},
}

// DeviceQueryNames lists the catalog, sorted.
func DeviceQueryNames() []string {
	out := make([]string, 0, len(deviceQueries))
	for name := range deviceQueries {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// NormalizeDeviceQuery checks a request against the catalog and fills in
// defaults. Unknown queries, unknown parameters and out-of-range values are
// refused, never coerced.
func NormalizeDeviceQuery(req DeviceQueryRequest) (DeviceQueryRequest, error) {
	spec, ok := deviceQueries[req.Query]
	if !ok {
		return req, fmt.Errorf("unknown device query %q (known: %s)", req.Query, strings.Join(DeviceQueryNames(), ", "))
	}
	out := DeviceQueryRequest{Query: req.Query, Params: map[string]string{}}
	for name := range req.Params {
		if _, ok := spec[name]; !ok {
			return req, fmt.Errorf("query %s takes no parameter %q", req.Query, name)
		}
	}
	for name, p := range spec {
		v, given := req.Params[name]
		v = strings.TrimSpace(v)
		if !given || v == "" {
			if p.def != "" {
				out.Params[name] = p.def
			}
			continue
		}
		switch {
		case len(p.enum) > 0:
			found := false
			for _, e := range p.enum {
				if v == e {
					found = true
					break
				}
			}
			if !found {
				return req, fmt.Errorf("%s must be one of %s", name, strings.Join(p.enum, ", "))
			}
		case p.max > 0:
			n, err := strconv.Atoi(v)
			if err != nil || n < p.min || n > p.max {
				return req, fmt.Errorf("%s must be a whole number from %d to %d", name, p.min, p.max)
			}
			v = strconv.Itoa(n)
		case p.pattern != nil:
			if !p.pattern.MatchString(v) {
				return req, fmt.Errorf("%s may hold only letters, digits, spaces and . _ @ : + - (up to 64)", name)
			}
		}
		out.Params[name] = v
	}
	if len(out.Params) == 0 {
		out.Params = nil
	}
	return out, nil
}

// DeviceQueryInt reads an integer parameter of a normalized request.
func DeviceQueryInt(req DeviceQueryRequest, name string) int {
	n, _ := strconv.Atoi(req.Params[name])
	return n
}
