package recruiting

import (
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/lib/introspect"
)

const (
	TypeProbeStart      = "recruiting.probe.start"
	TypeProbeSchedule   = "recruiting.probe.schedule"
	TypeProbeStatus     = "recruiting.probe.status"
	TypeExecutionResult = executioncontract.TypeResult
	typeProbeDue        = "recruiting.probe.due"
)

func manifest() introspect.Manifest {
	return introspect.Manifest{
		Class: Class, Interfaces: []string{"actor", "recruiting-control"},
		Words: map[string]introspect.WordSpec{
			TypeCompanyImport:          {Description: "start a Resource-backed company import preview with immutable hash fencing"},
			TypeCompanyImportGet:       {Description: "get one durable company import aggregate"},
			TypeCompanyImportItems:     {Description: "seek-page company import preview items and dispositions"},
			TypeCompanyAdd:             {Description: "add a recruiting company with an idempotent command"},
			TypeCompanyUpdate:          {Description: "update company identity fields with version fencing"},
			TypeCompanyPause:           {Description: "pause a company using an explicit drain policy"},
			TypeCompanyResume:          {Description: "resume a paused company"},
			TypeCompanyArchive:         {Description: "archive a company without deleting history"},
			TypeCompanyRestore:         {Description: "restore an archived company into paused validation"},
			TypeCompanyGet:             {Description: "get one recruiting company"},
			TypeCompanyList:            {Description: "seek-page recruiting companies"},
			TypeSourceAdd:              {Description: "add a recruitment source owned by one company"},
			TypeSourceUpdate:           {Description: "stage a corrected source endpoint with version fencing"},
			TypeSourceValidate:         {Description: "begin validation of a staged source endpoint"},
			TypeSourcePause:            {Description: "pause one recruitment source with an explicit drain policy"},
			TypeSourceResume:           {Description: "resume a paused recruitment source"},
			TypeSourceArchive:          {Description: "archive a recruitment source without deleting history"},
			TypeSourceRestore:          {Description: "restore an archived source into paused validation"},
			TypeSourceGet:              {Description: "get one recruitment source"},
			TypeSourceList:             {Description: "seek-page sources globally or for one company"},
			TypeJobGet:                 {Description: "get one source job"},
			TypeJobList:                {Description: "seek-page jobs belonging to one recruitment source"},
			TypeWorkGet:                {Description: "get one operational work item"},
			TypeWorkList:               {Description: "seek-page operational work or list runnable work by capability"},
			TypeWorkCreate:             {Description: "create one manually initiated operational work item"},
			TypeWorkPause:              {Description: "pause work and fence results from existing attempts"},
			TypeWorkResume:             {Description: "resume paused work as open"},
			TypeWorkRetry:              {Description: "create a new causal work lifecycle from terminal non-success work"},
			TypeWorkCancel:             {Description: "cancel non-terminal work and fence existing attempts"},
			TypeWorkResolve:            {Description: "record an explicit human non-success resolution"},
			TypeRunJoinOccurrence:      {Description: "materialize one planned daily source occurrence for immediate manual execution"},
			TypeRunDiagnostic:          {Description: "run one source listing recipe and retain evidence without changing jobs or checkpoint"},
			TypeRunProduction:          {Description: "run one independent production listing sync with checkpoint compare-and-swap"},
			TypeExecutionOffer:         {Description: "offer one capability-matched listing or detail execution to an authenticated executor"},
			TypeExecutionAccept:        {Description: "accept a bound execution attempt using executor incarnation fencing"},
			TypeExecutionStarted:       {Description: "atomically start an attempt, its work, and an associated listing occurrence when present"},
			TypeExecutionFailed:        {Description: "record classified failure evidence and apply versioned retry or human routing"},
			TypeExecutionWakeCompleted: {Description: "acknowledge one durable executor wake after it becomes idle or handles an attempt"},
			TypeDailyRunGet:            {Description: "get one immutable daily coverage run"},
			TypeDailyRunList:           {Description: "seek-page immutable daily coverage runs"},
			TypeDailyRunSummary:        {Description: "inspect live coverage counts and seek-page source occurrences"},
			TypeSystemStatus:           {Description: "inspect a coherent operational snapshot of work, attempts, daily runs, repairs, and delivery backlogs"},
			TypeCapacityStatus:         {Description: "inspect runnable queue pressure, active budget usage, configured limits, and executor fleet capacity"},
			TypeSystemReconcile:        {Description: "deliver one bounded batch of recruiting outbox events to the Atoll ledger"},
			TypeProbeStart:             {Description: "create and dispatch a durable recruiting P0 probe work"},
			TypeProbeSchedule:          {Description: "schedule a durable recruiting P0 probe occurrence"},
			TypeProbeStatus:            {Description: "inspect P0 probe work owned by the recruiting actor"},
			TypeExecutionResult:        {Description: "submit an executor result for actor-owned recruiting work"},
		},
	}
}
