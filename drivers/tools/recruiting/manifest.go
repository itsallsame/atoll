package recruiting

import (
	"encoding/json"

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
	result := introspect.Manifest{
		Class: Class, Interfaces: []string{"actor", "recruiting-control"},
		Words: map[string]introspect.WordSpec{
			TypeCompanyMergePreview:            {Description: "preview a version-fenced reversible logical company merge or reversal without rewriting history"},
			TypeCompanyMergeConfirm:            {Description: "confirm an exact logical company merge preview and activate or close alias intervals"},
			TypeCompanyImport:                  {Description: "start a Resource-backed company import preview with immutable hash fencing"},
			TypeCompanyImportGet:               {Description: "get one durable company import aggregate"},
			TypeCompanyImportItems:             {Description: "seek-page company import preview items and dispositions"},
			TypeCompanyImportConfirm:           {Description: "confirm an exact company import preview and start bounded item application"},
			TypeCompanyImportCancel:            {Description: "cancel a company import and fence in-flight application"},
			TypeCompanyImportItemResolve:       {Description: "retry or explicitly skip one waiting company import item and reaggregate its parent"},
			TypeCompanyAdd:                     {Description: "add a recruiting company with an idempotent command"},
			TypeCompanyUpdate:                  {Description: "update company identity fields with version fencing"},
			TypeCompanyWebsiteRollback:         {Description: "restore the immutable previous website of the current Company website revision and append a new review revision"},
			TypeCompanyPause:                   {Description: "pause a company using an explicit drain policy"},
			TypeCompanyResume:                  {Description: "resume a paused company"},
			TypeCompanyArchive:                 {Description: "archive a company without deleting history"},
			TypeCompanyRestore:                 {Description: "restore an archived company into paused validation"},
			TypeCompanyGet:                     {Description: "get one recruiting company"},
			TypeCompanyList:                    {Description: "seek-page recruiting companies"},
			TypeCompanyErasurePreview:          {Description: "start a bounded immutable compliance-erasure impact preview for an archived Company"},
			TypeCompanyErasureGet:              {Description: "get one durable Company compliance-erasure request and its control Work"},
			TypeCompanyErasureApprove:          {Description: "approve an exact frozen compliance-erasure preview as a distinct authenticated operator"},
			TypeCompanyErasureResources:        {Description: "seek-page the immutable Artifact Resource cleanup manifest for a Company erasure"},
			TypeCompanyErasureVerifyAbsent:     {Description: "verify through the Resource plane that an Artifact object is explicitly absent and record the cleanup fact"},
			TypeSourceAdd:                      {Description: "add a recruitment source owned by one company"},
			TypeSourceUpdate:                   {Description: "stage a corrected source endpoint with version fencing"},
			TypeSourceValidate:                 {Description: "atomically start a staged source validation Work with a frozen listing Recipe execution"},
			TypeSourceValidationPublish:        {Description: "atomically publish a validated endpoint, listing assignment, evidence assessment, and audit fact"},
			TypeSourceValidationReject:         {Description: "reject a staged source candidate while retaining validation evidence and any active endpoint"},
			TypeSourcePause:                    {Description: "pause one recruitment source with an explicit drain policy"},
			TypeSourceResume:                   {Description: "resume a paused recruitment source"},
			TypeSourceArchive:                  {Description: "archive a recruitment source without deleting history"},
			TypeSourceRestore:                  {Description: "restore an archived source into paused validation"},
			TypeSourceReassignPreview:          {Description: "preview a version-fenced cross-company Source reassignment without rewriting historical ownership"},
			TypeSourceReassignConfirm:          {Description: "confirm an exact Source reassignment preview and create an independently validated successor or split Source"},
			TypeSourceDiscover:                 {Description: "explicitly start one version-fenced source discovery generation for a company"},
			TypeSourceDiscoveryGet:             {Description: "get one durable source discovery generation"},
			TypeSourceDiscoveryCandidates:      {Description: "seek-page independently reviewable candidates from one source discovery"},
			TypeSourceDiscoveryCandidateAccept: {Description: "independently accept one discovered candidate and atomically create its candidate Source"},
			TypeSourceDiscoveryCandidateReject: {Description: "independently reject one discovered candidate with an audited reason"},
			TypeDeepDiscoveryStart:             {Description: "start a durable agent-driven deep discovery mission for one Company; search and browser remain bounded sensors"},
			TypeDeepDiscoveryGet:               {Description: "inspect one durable deep discovery mission, its stage, budget, coverage, and next state"},
			TypeDeepDiscoveryCheckpoint:        {Description: "atomically append one bounded evidence-graph delta and advance at most one deep discovery stage"},
			TypeDeepDiscoveryGraph:             {Description: "seek-page the durable evidence nodes and relationship edges of one deep discovery mission"},
			TypeDeepDiscoveryGuide:             {Description: "get the stage-specific Deep Discovery playbook so an Agent combines search, official navigation, browser, network, and coverage review instead of guessing"},
			TypeDeepDiscoveryWait:              {Description: "route an ambiguous or budget-blocked deep discovery mission to explicit human attention"},
			TypeDeepDiscoveryResume:            {Description: "resume a waiting deep discovery mission without losing its evidence or checkpoint"},
			TypeDeepDiscoveryComplete:          {Description: "complete deep discovery only after coverage review, no critical gaps, and validated list candidates"},
			TypeDeepDiscoveryCancel:            {Description: "cancel an active or waiting Deep Discovery mission, retain its evidence, and release the Company for a new generation"},
			TypeDeepDiscoveryBrowserObserve:    {Description: "queue one bounded read-only public-browser sensor Work inside a Deep Discovery Mission; execution is asynchronous and budgeted"},
			TypeDeepDiscoveryBrowserGet:        {Description: "inspect one Deep Discovery browser probe and its bounded terminal links/evidence"},
			TypeDeepDiscoveryPublicQueryVerify: {Description: "queue one independent http.fetch verification for exact public-query evidence from a completed browser Probe"},
			TypeDeepDiscoveryPublicQueryGet:    {Description: "inspect one public-query verification, its Work, and immutable response Artifact"},
			TypePublicQueryInspect:             {Description: "inspect a bounded structural preview of one verified public JSON response so an Agent can derive evidence-backed field paths without direct Resource access"},
			TypeOnboardingBegin:                {Description: "preferred natural-language entry: begin or resume typed recruitment-URL discovery from only a company name; after calling, follow agent_directive until completed or waiting_human"},
			TypeOnboardingStatus:               {Description: "look up company-name onboarding and initialization progress, typed URLs, candidate Sources, and the next evidence-backed action; no internal ID is required"},
			TypeOnboardingAdvance:              {Description: "advance one safe asynchronous discovery step from only a company name; the control plane derives Probe, Verification, Work, and Stub identities from persisted evidence"},
			TypeOnboardingRepairValidate:       {Description: "after an operator confirms a discovery execution repair, create one new budgeted causal browser Probe using only company name and reason"},
			TypeOnboardingMaterialize:          {Description: "materialize every typed validated URL as an idempotent candidate Source and move a new Company into discovery; then initialize Sources through validated Recipes"},
			TypeSourceGet:                      {Description: "get one recruitment source"},
			TypeSourceList:                     {Description: "seek-page sources globally or for one company"},
			TypeSourceEndpointHistory:          {Description: "inspect immutable staged and evidence-activated endpoint redirect or correction history"},
			TypeSourceProfileBind:              {Description: "bind future listing or detail Browser work to a ready Profile on its authorized device"},
			TypeSourceProfileUnbind:            {Description: "remove a versioned Source Profile binding after its Recipe no longer requires that Profile"},
			TypeRecipeInspect:                  {Description: "inspect one immutable Recipe version, optional browser-capture provenance, and its current Source assignment count"},
			TypeRecipePrepare:                  {Description: "compile a bounded Listing Recipe from exact public-query evidence and a small site-specific semantic mapping; the control plane owns the Recipe ABI, request, budgets, and incremental defaults"},
			TypeRecipePropose:                  {Description: "strictly validate and register an immutable Recipe draft; the first Listing Recipe may bind the exact candidate endpoint without publishing the Source"},
			TypeRecipeValidate:                 {Description: "run Listing, Detail, or Discovery Recipe candidates against frozen real evidence; first Listing/Detail validation supports cold start without premature publication"},
			TypeRecipeApprove:                  {Description: "activate one validating Recipe only after a complete executor-succeeded real-sample validation"},
			TypeRecipeReject:                   {Description: "return one validating Recipe to draft after its exact validation Work is closed"},
			TypeRecipeAssign:                   {Description: "atomically create the first Detail Recipe assignment for a ready Source"},
			TypeRecipeQuarantine:               {Description: "atomically quarantine one active Recipe version without rewriting Source assignments"},
			TypeRecipeRollout:                  {Description: "atomically roll one Source detail assignment to a compatible active Recipe with source and assignment version fencing"},
			TypeRecipeRolloutBatch:             {Description: "create and materialize a Resource-backed deterministic Recipe rollout preview"},
			TypeRecipeRolloutBatchGet:          {Description: "get one durable Recipe rollout batch and its parent control Work"},
			TypeRecipeRolloutBatchItems:        {Description: "seek-page frozen Source members and rollout outcomes"},
			TypeRecipeRolloutBatchConfirm:      {Description: "confirm an exact Recipe rollout preview and open only its canary"},
			TypeRecipeRolloutBatchResume:       {Description: "explicitly retry a paused Recipe rollout wave after failed validation work is resolved"},
			TypeRecipeRolloutBatchRollback:     {Description: "roll back the complete published prefix of a paused Recipe rollout batch"},
			TypeRecipeRolloutBatchCancel:       {Description: "cancel a Recipe rollout batch and release its active scope key"},
			TypeRecipeRollback:                 {Description: "append a new Source detail assignment from immutable assignment history with version fencing"},
			TypeBaselineStart:                  {Description: "start one immutable, version-fenced baseline listing generation"},
			TypeJobGet:                         {Description: "get one source job"},
			TypeJobList:                        {Description: "seek-page jobs belonging to one recruitment source"},
			TypeJobCorrect:                     {Description: "atomically set or clear one version-fenced manual Job field override with audit history"},
			TypeJobCorrectionGet:               {Description: "inspect the current manual override and bounded immutable history for one Job field"},
			TypeBackfillCreate:                 {Description: "create a version-fenced historical Artifact recompute or present-time live refetch preview"},
			TypeBackfillGet:                    {Description: "inspect one durable historical backfill and its parent control Work"},
			TypeBackfillItems:                  {Description: "seek-page immutable historical backfill selections and outcomes"},
			TypeBackfillOutputs:                {Description: "seek-page bounded historical backfill output metadata without loading result bodies"},
			TypeBackfillOutputGet:              {Description: "inspect one bounded historical backfill output body and immutable lineage"},
			TypeBackfillGaps:                   {Description: "seek-page failed and operator-accepted historical backfill gaps"},
			TypeBackfillConfirm:                {Description: "confirm an exact immutable historical backfill preview before child execution is queued"},
			TypeBackfillPause:                  {Description: "drain-pause a running historical backfill while allowing already-running child attempts to settle"},
			TypeBackfillResume:                 {Description: "resume an operator-paused historical backfill with no unresolved failed items"},
			TypeBackfillCancel:                 {Description: "cancel a historical backfill preview before child execution begins"},
			TypeBackfillItemResolve:            {Description: "accept one audited backfill gap or create a causal retry Work for a failed item"},
			TypeProfileRegister:                {Description: "register a secret-free Browser Profile slot and open its mandatory device authentication repair"},
			TypeProfileGet:                     {Description: "inspect secret-free Browser Profile metadata and its latest repair session"},
			TypeProfileRepairBegin:             {Description: "create one expiring Profile repair session routed only to its authorized device"},
			TypeWorkGet:                        {Description: "get one operational work item"},
			TypeWorkList:                       {Description: "seek-page operational work or list runnable work by capability"},
			TypeWorkCreate:                     {Description: "create one manually initiated operational work item"},
			TypeWorkPause:                      {Description: "pause work and fence results from existing attempts"},
			TypeWorkResume:                     {Description: "resume paused work as open"},
			TypeWorkCorrect:                    {Description: "version-fence and correct scheduling metadata of an unclaimed open work"},
			TypeWorkRetry:                      {Description: "create a new causal work lifecycle from terminal non-success work"},
			TypeWorkCancel:                     {Description: "cancel non-terminal work and fence existing attempts"},
			TypeWorkResolve:                    {Description: "record an explicit human non-success resolution"},
			TypeRunJoinOccurrence:              {Description: "materialize one planned daily source occurrence for immediate manual execution"},
			TypeRunDiagnostic:                  {Description: "run one source listing recipe and retain evidence without changing jobs or checkpoint"},
			TypeRunProduction:                  {Description: "run one production listing sync, optionally compensating a closed daily occurrence"},
			TypeExecutionOffer:                 {Description: "offer one capability-matched listing or detail execution to an authenticated executor"},
			TypeExecutionOfferBatch:            {Description: "offer a bounded supply batch while preserving one fenced Attempt per Work"},
			TypeExecutionClaimBatch:            {Description: "accept and start a bounded supply batch with replayable per-Attempt receipts"},
			TypeExecutionResultBatch:           {Description: "submit bounded independent execution results in one application envelope"},
			TypeExecutionAccept:                {Description: "accept a bound execution attempt using executor incarnation fencing"},
			TypeExecutionStarted:               {Description: "atomically start an attempt, its work, and an associated listing occurrence when present"},
			TypeExecutionFailed:                {Description: "record classified failure evidence and apply versioned retry or human routing"},
			TypeExecutionWakeCompleted:         {Description: "acknowledge one durable executor wake after it becomes idle or handles an attempt"},
			TypeDailyRunGet:                    {Description: "get one immutable daily coverage run"},
			TypeDailyRunList:                   {Description: "seek-page immutable daily coverage runs"},
			TypeDailyRunSummary:                {Description: "inspect live coverage counts and seek-page source occurrences"},
			TypeDailyRunOccurrenceExclude:      {Description: "explicitly exclude one still-planned occurrence without rewriting the daily roster"},
			TypeRepairGet:                      {Description: "inspect one shared repair incident and seek-page its affected operational work"},
			TypeRepairList:                     {Description: "seek-page shared repair incidents by lifecycle status"},
			TypeRepairValidate:                 {Description: "bind an open shared repair to one executor-succeeded causal validation Work"},
			TypeRepairResolve:                  {Description: "resolve a validated shared repair and complete its unique Repair Work"},
			TypeRepairRecover:                  {Description: "recover at most 100 still-blocked affected Works in one version-fenced batch"},
			TypeSystemStatus:                   {Description: "inspect a coherent operational snapshot of work, attempts, daily runs, repairs, and delivery backlogs"},
			TypeScopeControlGet:                {Description: "inspect one durable Company or Source pause/resume operation and seek-page its catch-up decisions"},
			TypeCapacityStatus:                 {Description: "inspect runnable queue pressure, active budget usage, configured limits, and executor fleet capacity"},
			TypeSystemReconcile:                {Description: "deliver one bounded batch of recruiting outbox events to the Atoll ledger"},
			TypeProbeStart:                     {Description: "create and dispatch a durable recruiting P0 probe work"},
			TypeProbeSchedule:                  {Description: "schedule a durable recruiting P0 probe occurrence"},
			TypeProbeStatus:                    {Description: "inspect P0 probe work owned by the recruiting actor"},
			TypeExecutionResult:                {Description: "submit an executor result for actor-owned recruiting work"},
		},
	}
	// These are the public words used by the natural-language onboarding path.
	// Publishing their exact wire shapes is essential: an Atoll agent can only
	// call another actor safely from actor.describe; it must never have to infer
	// application payload fields from validation failures.
	inputSchemas := map[string]string{
		TypeCompanyAdd:    `{"type":"object","additionalProperties":false,"required":["command_id","company_id","name","reason"],"properties":{"command_id":{"type":"string"},"company_id":{"type":"string"},"name":{"type":"string"},"website":{"type":"string","description":"Optional canonical http(s) official website; omit when it is not known yet."},"reason":{"type":"string"}}}`,
		TypeCompanyGet:    `{"type":"object","additionalProperties":false,"required":["company_id"],"properties":{"company_id":{"type":"string"}}}`,
		TypeCompanyList:   `{"type":"object","additionalProperties":false,"properties":{"cursor":{"type":"string"},"limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeCompanyUpdate: mutationSchema(`,"name":{"type":"string"},"website":{"type":"string"}`),
		TypeSourceDiscover: mutationSchema(`,"discovery_id":{"type":"string"},"work_id":{"type":"string"},"discovery_generation":{"type":"integer","minimum":1},"recipe_id":{"type":"string"},"recipe_version":{"type":"integer","minimum":1},"profile_id":{"type":"string"},"priority":{"type":"integer","minimum":-100,"maximum":100},"deadline_at":{"type":"string"}`,
			"discovery_id", "work_id", "discovery_generation", "recipe_id", "recipe_version"),
		TypeSourceDiscoveryGet:        `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeSourceDiscoveryCandidates: `{"type":"object","additionalProperties":false,"required":["discovery_id"],"properties":{"discovery_id":{"type":"string"},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeDeepDiscoveryStart: mutationSchema(`,"mission_id":{"type":"string"},"discovery_generation":{"type":"integer","minimum":1},"max_search_rounds":{"type":"integer","minimum":1,"maximum":20},"max_operations":{"type":"integer","minimum":1,"maximum":2000}`,
			"mission_id", "discovery_generation"),
		TypeSourceAdd:               `{"type":"object","additionalProperties":false,"required":["command_id","source_id","company_id","endpoint","discovery_generation","reason"],"properties":{"command_id":{"type":"string"},"source_id":{"type":"string"},"company_id":{"type":"string"},"endpoint":{"type":"string"},"category":{"type":"string"},"discovery_generation":{"type":"integer","minimum":1},"reason":{"type":"string"}}}`,
		TypeDeepDiscoveryGet:        `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeDeepDiscoveryGraph:      `{"type":"object","additionalProperties":false,"required":["mission_id"],"properties":{"mission_id":{"type":"string"},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeDeepDiscoveryGuide:      `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeDeepDiscoveryCheckpoint: deepDiscoveryCheckpointSchema(),
		TypeDeepDiscoveryWait:       mutationSchema(`,"waiting_reason":{"type":"string"}`, "waiting_reason"),
		TypeDeepDiscoveryResume:     mutationSchema(``),
		TypeDeepDiscoveryComplete:   mutationSchema(``),
		TypeDeepDiscoveryCancel:     mutationSchema(``),
		TypeDeepDiscoveryBrowserObserve: mutationSchema(`,"probe_id":{"type":"string"},"work_id":{"type":"string"},"url":{"type":"string"},"wait_selector":{"type":"string"},"scroll_repeats":{"type":"integer","minimum":0,"maximum":10},"follow_link_selector":{"type":"string"},"priority":{"type":"integer","minimum":-100,"maximum":100},"deadline_at":{"type":"string"},"stub_verification_ids":{"type":"array","maxItems":10,"items":{"type":"string"}}`,
			"probe_id", "work_id", "url"),
		TypeDeepDiscoveryBrowserGet: `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeDeepDiscoveryPublicQueryVerify: mutationSchema(`,"verification_id":{"type":"string"},"probe_id":{"type":"string"},"endpoint_url":{"type":"string"},"body_hash":{"type":"string"},"work_id":{"type":"string"},"priority":{"type":"integer","minimum":-100,"maximum":100},"deadline_at":{"type":"string"}`,
			"verification_id", "probe_id", "endpoint_url", "body_hash", "work_id"),
		TypeDeepDiscoveryPublicQueryGet: `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeOnboardingBegin:             `{"type":"object","additionalProperties":false,"required":["company_name"],"properties":{"company_name":{"type":"string"},"website":{"type":"string","description":"Optional confirmed official website; omit rather than guess."},"max_search_rounds":{"type":"integer","minimum":1,"maximum":20},"max_operations":{"type":"integer","minimum":1,"maximum":2000}}}`,
		TypeOnboardingStatus:            `{"type":"object","additionalProperties":false,"required":["company_name"],"properties":{"company_name":{"type":"string"}}}`,
		TypeOnboardingAdvance:           `{"type":"object","additionalProperties":false,"required":["company_name"],"properties":{"company_name":{"type":"string"}}}`,
		TypeOnboardingRepairValidate:    `{"type":"object","additionalProperties":false,"required":["company_name","reason"],"properties":{"company_name":{"type":"string"},"reason":{"type":"string"}}}`,
		TypeOnboardingMaterialize:       `{"type":"object","additionalProperties":false,"required":["company_name"],"properties":{"company_name":{"type":"string"}}}`,
		TypePublicQueryInspect:          `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeSourceGet:                   `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeSourceList:                  `{"type":"object","additionalProperties":false,"properties":{"company_id":{"type":"string"},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeSourceUpdate:                mutationSchema(`,"endpoint":{"type":"string"},"category":{"type":"string"},"transition_kind":{"type":"string","enum":["redirect","correction"]}`),
		TypeSourceValidate: mutationSchema(`,"recipe_id":{"type":"string"},"recipe_version":{"type":"integer","minimum":1},"expected_assignment_version":{"type":"integer","minimum":0},"run_id":{"type":"string"},"work_id":{"type":"string"},"profile_id":{"type":"string"},"priority":{"type":"integer","minimum":-100,"maximum":100},"deadline_at":{"type":"string"}`,
			"recipe_id", "recipe_version", "expected_assignment_version", "run_id", "work_id"),
		TypeSourceValidationPublish: sourceValidationPublishSchema(),
		TypeRecipeInspect:           `{"type":"object","additionalProperties":false,"required":["recipe_id","recipe_version"],"properties":{"recipe_id":{"type":"string"},"recipe_version":{"type":"integer","minimum":1}}}`,
		TypeRecipePrepare:           recipePrepareSchema(),
		TypeRecipePropose: mutationSchema(`,"recipe_id":{"type":"string"},"recipe_version":{"type":"integer","minimum":1},"endpoint_revision":{"type":"integer","minimum":0},"content_ref":{"type":"string"},"expected_content_hash":{"type":"string"},"capture_ref":{"type":"string"},"expected_capture_hash":{"type":"string"}`,
			"recipe_id", "recipe_version", "content_ref", "expected_content_hash"),
		TypeRecipeValidate: mutationSchema(`,"recipe_version":{"type":"integer","minimum":1},"source_id":{"type":"string"},"company_id":{"type":"string"},"run_id":{"type":"string"},"work_id":{"type":"string"},"sample_job_id":{"type":"string"},"profile_id":{"type":"string"},"priority":{"type":"integer","minimum":-100,"maximum":100},"deadline_at":{"type":"string"}`,
			"recipe_version", "run_id", "work_id"),
		TypeRecipeApprove: mutationSchema(`,"recipe_version":{"type":"integer","minimum":1},"validation_work_id":{"type":"string"}`,
			"recipe_version", "validation_work_id"),
		TypeRecipeAssign: mutationSchema(`,"recipe_id":{"type":"string"},"recipe_version":{"type":"integer","minimum":1}`,
			"recipe_id", "recipe_version"),
		TypeBaselineStart: mutationSchema(`,"expected_company_version":{"type":"integer","minimum":1},"work_id":{"type":"string"},"baseline_generation":{"type":"integer","minimum":1},"profile_id":{"type":"string"},"priority":{"type":"integer","minimum":-100,"maximum":100},"deadline_at":{"type":"string"}`,
			"expected_company_version", "work_id", "baseline_generation"),
		TypeWorkGet:  `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"}}}`,
		TypeWorkList: `{"type":"object","additionalProperties":false,"properties":{"status":{"type":"string"},"capability":{"type":"string"},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeWorkResolve: mutationSchema(`,"resolution":{"type":"string","enum":["accepted_gap","skipped","terminated"]}`,
			"resolution"),
		TypeWorkRetry: mutationSchema(`,"new_work_id":{"type":"string"},"not_before":{"type":"string"},"deadline_at":{"type":"string"}`,
			"new_work_id"),
		TypeWorkCancel:      mutationSchema(``),
		TypeRepairGet:       `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string"},"affected_cursor":{"type":"string"},"affected_limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeRepairList:      `{"type":"object","additionalProperties":false,"properties":{"status":{"type":"string"},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":0,"maximum":500}}}`,
		TypeRepairValidate:  mutationSchema(`,"validation_work_id":{"type":"string"}`, "validation_work_id"),
		TypeRepairResolve:   mutationSchema(`,"resolution":{"type":"string"}`, "resolution"),
		TypeRepairRecover:   mutationSchema(`,"limit":{"type":"integer","minimum":1,"maximum":100}`, "limit"),
		TypeSystemStatus:    `{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":0}}}`,
		TypeSystemReconcile: `{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":0}}}`,
		TypeCapacityStatus:  `{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":0}}}`,
	}
	for word, schema := range inputSchemas {
		spec := result.Words[word]
		spec.InputSchema = json.RawMessage(schema)
		result.Words[word] = spec
	}
	return result
}

func deepDiscoveryCheckpointSchema() string {
	coverage := `{"type":"object","additionalProperties":false,"required":["identity_scoped","brands_reviewed","sites_enumerated","sites_explored","pools_detected","candidates_validated","blindspots_reviewed","critical_gap_count"],"properties":{"identity_scoped":{"type":"boolean"},"brands_reviewed":{"type":"boolean"},"sites_enumerated":{"type":"boolean"},"sites_explored":{"type":"boolean"},"pools_detected":{"type":"boolean"},"candidates_validated":{"type":"boolean"},"blindspots_reviewed":{"type":"boolean"},"critical_gap_count":{"type":"integer","minimum":0}}}`
	node := `{"type":"object","additionalProperties":false,"required":["ref","kind","canonical_value","label","state","sensor","basis"],"properties":{"ref":{"type":"string","description":"Checkpoint-local readable handle used by edges; the Actor generates the durable node ID."},"kind":{"type":"string","enum":["company","brand","legal_entity","domain","site","listing_pool","api_endpoint","list_url","blindspot"]},"canonical_value":{"type":"string"},"label":{"type":"string"},"state":{"type":"string","enum":["candidate","validated","rejected","excluded"]},"sensor":{"type":"string","enum":["web_search","official_site","sitemap","robots","browser","network","ats_fingerprint","detail_reverse","human"]},"evidence_url":{"type":"string"},"evidence_artifact_id":{"type":"string"},"basis":{"type":"string"},"recruitment_type":{"type":"string","enum":["social","campus","intern","special","all","unknown"],"description":"Required and non-unknown for a validated list_url; classify from page or network evidence, never from URL spelling alone."},"special_program":{"type":"string","description":"Required only when recruitment_type is special; retain the programme name shown by the site."}}}`
	edge := `{"type":"object","additionalProperties":false,"required":["from","to","relation","basis"],"properties":{"from":{"type":"string","description":"Incoming node ref or an existing durable node ID."},"to":{"type":"string","description":"Incoming node ref or an existing durable node ID."},"relation":{"type":"string"},"basis":{"type":"string"}}}`
	return mutationSchema(`,"stage":{"type":"string","enum":["scope_building","brand_expansion","site_enumeration","site_exploration","pool_detection","candidate_validation","coverage_review"]},"summary":{"type":"string"},"search_rounds":{"type":"integer","minimum":0},"operations":{"type":"integer","minimum":0},"coverage":`+coverage+`,"nodes":{"type":"array","maxItems":200,"items":`+node+`},"edges":{"type":"array","maxItems":400,"items":`+edge+`}`,
		"stage", "summary", "coverage", "nodes", "edges")
}

func recipePrepareSchema() string {
	offsetPagination := `{"type":"object","additionalProperties":false,"properties":{"offset_pointer":{"type":"string","description":"JSON pointer to the response offset for metadata pagination."},"limit_pointer":{"type":"string","description":"JSON pointer to the response page limit for metadata pagination."},"total_pointer":{"type":"string","description":"JSON pointer to the response total for metadata pagination."},"offset_query":{"type":"string","description":"Observed URL query parameter carrying the offset; do not guess."},"limit_query":{"type":"string","description":"Observed URL query parameter carrying the page size; do not guess."},"offset_body_field":{"type":"string","description":"Observed top-level request JSON field carrying the offset."},"limit_body_field":{"type":"string","description":"Observed top-level request JSON field carrying the page size."},"page_size":{"type":"integer","minimum":1,"maximum":500,"description":"Exact page size present in the observed request; required for short-page pagination."}}}`
	mapping := `{"type":"object","additionalProperties":false,"required":["identity_pointer","detail_url_pointer"],"properties":{"collection":{"type":"string","description":"JSON pointer to the job array in the verified response preview."},"collection_root":{"type":"boolean","const":true,"description":"True only when the verified response root itself is the job array."},"identity_pointer":{"type":"string","description":"JSON pointer, relative to one job item, for its stable job ID."},"detail_url_pointer":{"type":"string","description":"JSON pointer, relative to one job item, for its absolute detail URL or the ID used by detail_url_template."},"detail_url_template":{"type":"string","description":"Optional absolute URL containing exactly one {value}; use only when detail_url_pointer yields an ID rather than a URL."},"title_pointer":{"type":"string","description":"Optional item-relative JSON pointer for job title."},"activity_pointer":{"type":"string","description":"Optional item-relative JSON pointer for published/updated activity time."},"activity_time_format":{"type":"string","enum":["rfc3339","utc_datetime"],"description":"Required only when activity_pointer has one of these verified formats."},"exclude_pinned_pointer":{"type":"string","description":"Optional item-relative JSON pointer identifying pinned jobs that must not define the incremental boundary."},"boundary_mode":{"type":"string","enum":["frontier_keys","activity_time"],"description":"Defaults to frontier_keys; activity_time requires activity_pointer."},"offset_pagination":` + offsetPagination + `},"oneOf":[{"required":["collection"],"not":{"required":["collection_root"]}},{"required":["collection_root"],"not":{"required":["collection"]}}]}`
	return `{"type":"object","additionalProperties":false,"required":["command_id","target","expected_version","reason","probe_id","body_hash","mapping"],"properties":{"command_id":{"type":"string"},"target":{"type":"object","additionalProperties":false,"required":["target_type","target_id"],"properties":{"target_type":{"type":"string","const":"source"},"target_id":{"type":"string"}}},"expected_version":{"type":"integer","minimum":1},"reason":{"type":"string"},"probe_id":{"type":"string"},"body_hash":{"type":"string"},"mapping":` + mapping + `}}`
}

func sourceValidationPublishSchema() string {
	return mutationSchema(`,"validation_work_id":{"type":"string","description":"Completed successful Source validation Work. The control plane derives the exact Recipe, Assignment fence, incremental policy, and evidence Artifacts from it."}`,
		"validation_work_id")
}

func mutationSchema(extraProperties string, extraRequired ...string) string {
	required := `"command_id","target","expected_version","reason"`
	for _, field := range extraRequired {
		required += `,"` + field + `"`
	}
	return `{"type":"object","additionalProperties":false,"required":[` + required + `],"properties":{"command_id":{"type":"string"},"target":{"type":"object","additionalProperties":false,"required":["target_type","target_id"],"properties":{"target_type":{"type":"string"},"target_id":{"type":"string"}}},"expected_version":{"type":"integer","minimum":1},"reason":{"type":"string"}` + extraProperties + `}}`
}
