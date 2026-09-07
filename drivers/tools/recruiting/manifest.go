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
			TypeCompanyAdd:      {Description: "add a recruiting company with an idempotent command"},
			TypeCompanyUpdate:   {Description: "update company identity fields with version fencing"},
			TypeCompanyPause:    {Description: "pause a company using an explicit drain policy"},
			TypeCompanyResume:   {Description: "resume a paused company"},
			TypeCompanyArchive:  {Description: "archive a company without deleting history"},
			TypeCompanyRestore:  {Description: "restore an archived company into paused validation"},
			TypeCompanyGet:      {Description: "get one recruiting company"},
			TypeCompanyList:     {Description: "seek-page recruiting companies"},
			TypeSourceGet:       {Description: "get one recruitment source"},
			TypeJobGet:          {Description: "get one source job"},
			TypeWorkGet:         {Description: "get one operational work item"},
			TypeWorkList:        {Description: "list runnable work by capability and optional resource constraints"},
			TypeDailyRunGet:     {Description: "get one immutable daily coverage run"},
			TypeSystemReconcile: {Description: "deliver one bounded batch of recruiting outbox events to the Atoll ledger"},
			TypeProbeStart:      {Description: "create and dispatch a durable recruiting P0 probe work"},
			TypeProbeSchedule:   {Description: "schedule a durable recruiting P0 probe occurrence"},
			TypeProbeStatus:     {Description: "inspect P0 probe work owned by the recruiting actor"},
			TypeExecutionResult: {Description: "submit an executor result for actor-owned recruiting work"},
		},
	}
}
