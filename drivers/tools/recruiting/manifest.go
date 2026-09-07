package recruiting

import "github.com/wanpengxie/atoll/lib/introspect"

const (
	TypeProbeStart      = "recruiting.probe.start"
	TypeProbeSchedule   = "recruiting.probe.schedule"
	TypeProbeStatus     = "recruiting.probe.status"
	TypeExecutionResult = "recruiting.execution.result"
	typeProbeDue        = "recruiting.probe.due"
)

func manifest() introspect.Manifest {
	return introspect.Manifest{
		Class: Class, Interfaces: []string{"actor", "recruiting-control"},
		Words: map[string]introspect.WordSpec{
			TypeProbeStart:      {Description: "create and dispatch a durable recruiting P0 probe work"},
			TypeProbeSchedule:   {Description: "schedule a durable recruiting P0 probe occurrence"},
			TypeProbeStatus:     {Description: "inspect P0 probe work owned by the recruiting actor"},
			TypeExecutionResult: {Description: "submit an executor result for actor-owned recruiting work"},
		},
	}
}
