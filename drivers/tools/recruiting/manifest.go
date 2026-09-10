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
			TypeCompanyImport:                  {Description: "start a Resource-backed company import preview with immutable hash fencing"},
			TypeCompanyImportGet:               {Description: "get one durable company import aggregate"},
			TypeCompanyImportItems:             {Description: "seek-page company import preview items and dispositions"},
			TypeCompanyImportConfirm:           {Description: "confirm an exact company import preview and start bounded item application"},
			TypeCompanyImportCancel:            {Description: "cancel a company import and fence in-flight application"},
			TypeCompanyAdd:                     {Description: "add a recruiting company with an idempotent command"},
			TypeCompanyUpdate:                  {Description: "update company identity fields with version fencing"},
			TypeCompanyPause:                   {Description: "pause a company using an explicit drain policy"},
			TypeCompanyResume:                  {Description: "resume a paused company"},
			TypeCompanyArchive:                 {Description: "archive a company without deleting history"},
			TypeCompanyRestore:                 {Description: "restore an archived company into paused validation"},
			TypeCompanyGet:                     {Description: "get one recruiting company"},
			TypeCompanyList:                    {Description: "seek-page recruiting companies"},
			TypeSourceAdd:                      {Description: "add a recruitment source owned by one company"},
			TypeSourceUpdate:                   {Description: "stage a corrected source endpoint with version fencing"},
			TypeSourceValidate:                 {Description: "atomically start a staged source validation Work with a frozen listing Recipe execution"},
			TypeSourceValidationPublish:        {Description: "atomically publish a validated endpoint, listing assignment, evidence assessment, and audit fact"},
			TypeSourceValidationReject:         {Description: "reject a staged source candidate while retaining validation evidence and any active endpoint"},
			TypeSourcePause:                    {Description: "pause one recruitment source with an explicit drain policy"},
			TypeSourceResume:                   {Description: "resume a paused recruitment source"},
			TypeSourceArchive:                  {Description: "archive a recruitment source without deleting history"},
			TypeSourceRestore:                  {Description: "restore an archived source into paused validation"},
			TypeSourceDiscover:                 {Description: "explicitly start one version-fenced source discovery generation for a company"},
			TypeSourceDiscoveryGet:             {Description: "get one durable source discovery generation"},
			TypeSourceDiscoveryCandidates:      {Description: "seek-page independently reviewable candidates from one source discovery"},
			TypeSourceDiscoveryCandidateAccept: {Description: "independently accept one discovered candidate and atomically create its candidate Source"},
			TypeSourceDiscoveryCandidateReject: {Description: "independently reject one discovered candidate with an audited reason"},
			TypeSourceGet:                      {Description: "get one recruitment source"},
			TypeSourceList:                     {Description: "seek-page sources globally or for one company"},
			TypeRecipeInspect:                  {Description: "inspect one immutable Recipe version, optional browser-capture provenance, and its current Source assignment count"},
			TypeRecipePropose:                  {Description: "strictly validate Source-bound Recipe and optional browser Capture Resources and atomically register an immutable draft"},
			TypeRecipeValidate:                 {Description: "run one draft or quarantined Listing Recipe against a frozen real Source without publishing business data"},
			TypeRecipeApprove:                  {Description: "activate one validating Recipe only after a complete executor-succeeded real-sample validation"},
			TypeRecipeReject:                   {Description: "return one validating Recipe to draft after its exact validation Work is closed"},
			TypeRecipeQuarantine:               {Description: "atomically quarantine one active Recipe version without rewriting Source assignments"},
			TypeRecipeRollout:                  {Description: "atomically roll one Source detail assignment to a compatible active Recipe with source and assignment version fencing"},
			TypeRecipeRolloutBatch:             {Description: "create and materialize a Resource-backed deterministic Recipe rollout preview"},
			TypeRecipeRolloutBatchGet:          {Description: "get one durable Recipe rollout batch and its parent control Work"},
			TypeRecipeRolloutBatchItems:        {Description: "seek-page frozen Source members and rollout outcomes"},
			TypeRecipeRolloutBatchConfirm:      {Description: "confirm an exact Recipe rollout preview and open only its canary"},
			TypeRecipeRolloutBatchCancel:       {Description: "cancel a Recipe rollout batch and release its active scope key"},
			TypeRecipeRollback:                 {Description: "append a new Source detail assignment from immutable assignment history with version fencing"},
			TypeBaselineStart:                  {Description: "start one immutable, version-fenced baseline listing generation"},
			TypeJobGet:                         {Description: "get one source job"},
			TypeJobList:                        {Description: "seek-page jobs belonging to one recruitment source"},
			TypeJobCorrect:                     {Description: "atomically set or clear one version-fenced manual Job field override with audit history"},
			TypeJobCorrectionGet:               {Description: "inspect the current manual override and bounded immutable history for one Job field"},
			TypeProfileGet:                     {Description: "inspect secret-free Browser Profile metadata and its latest repair session"},
			TypeProfileRepairBegin:             {Description: "create one expiring Profile repair session routed only to its authorized device"},
			TypeWorkGet:                        {Description: "get one operational work item"},
			TypeWorkList:                       {Description: "seek-page operational work or list runnable work by capability"},
			TypeWorkCreate:                     {Description: "create one manually initiated operational work item"},
			TypeWorkPause:                      {Description: "pause work and fence results from existing attempts"},
			TypeWorkResume:                     {Description: "resume paused work as open"},
			TypeWorkRetry:                      {Description: "create a new causal work lifecycle from terminal non-success work"},
			TypeWorkCancel:                     {Description: "cancel non-terminal work and fence existing attempts"},
			TypeWorkResolve:                    {Description: "record an explicit human non-success resolution"},
			TypeRunJoinOccurrence:              {Description: "materialize one planned daily source occurrence for immediate manual execution"},
			TypeRunDiagnostic:                  {Description: "run one source listing recipe and retain evidence without changing jobs or checkpoint"},
			TypeRunProduction:                  {Description: "run one production listing sync, optionally compensating a closed daily occurrence"},
			TypeExecutionOffer:                 {Description: "offer one capability-matched listing or detail execution to an authenticated executor"},
			TypeExecutionAccept:                {Description: "accept a bound execution attempt using executor incarnation fencing"},
			TypeExecutionStarted:               {Description: "atomically start an attempt, its work, and an associated listing occurrence when present"},
			TypeExecutionFailed:                {Description: "record classified failure evidence and apply versioned retry or human routing"},
			TypeExecutionWakeCompleted:         {Description: "acknowledge one durable executor wake after it becomes idle or handles an attempt"},
			TypeDailyRunGet:                    {Description: "get one immutable daily coverage run"},
			TypeDailyRunList:                   {Description: "seek-page immutable daily coverage runs"},
			TypeDailyRunSummary:                {Description: "inspect live coverage counts and seek-page source occurrences"},
			TypeRepairGet:                      {Description: "inspect one shared repair incident and seek-page its affected operational work"},
			TypeRepairList:                     {Description: "seek-page shared repair incidents by lifecycle status"},
			TypeRepairValidate:                 {Description: "bind an open shared repair to one executor-succeeded causal validation Work"},
			TypeRepairResolve:                  {Description: "resolve a validated shared repair and complete its unique Repair Work"},
			TypeRepairRecover:                  {Description: "recover at most 100 still-blocked affected Works in one version-fenced batch"},
			TypeSystemStatus:                   {Description: "inspect a coherent operational snapshot of work, attempts, daily runs, repairs, and delivery backlogs"},
			TypeCapacityStatus:                 {Description: "inspect runnable queue pressure, active budget usage, configured limits, and executor fleet capacity"},
			TypeSystemReconcile:                {Description: "deliver one bounded batch of recruiting outbox events to the Atoll ledger"},
			TypeProbeStart:                     {Description: "create and dispatch a durable recruiting P0 probe work"},
			TypeProbeSchedule:                  {Description: "schedule a durable recruiting P0 probe occurrence"},
			TypeProbeStatus:                    {Description: "inspect P0 probe work owned by the recruiting actor"},
			TypeExecutionResult:                {Description: "submit an executor result for actor-owned recruiting work"},
		},
	}
}
