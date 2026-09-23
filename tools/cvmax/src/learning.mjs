import {createHash, randomUUID} from 'node:crypto';
export const digest = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
export const LEARNING_VERSION = 4;
const modes = new Set(['user','prelearn']);
const episodeActiveMs = episode => Number.isFinite(episode.activeMs) ? Math.max(0, episode.activeMs) : 0;
const refreshActiveMs = learning => learning.activeMs=learning.episodes.reduce((sum,episode)=>sum+episodeActiveMs(episode),0);

// Stored under the owning Application, never in model memory or the public registry.
export function learningFor(application, mode = 'user') {
  if (!modes.has(mode)) throw Error('learning_mode_invalid');
  const learning=application.learning ??= {schemaVersion: LEARNING_VERSION, mode, activeMs: 0,
    episodes: [], observations: [], candidates: {}, unresolved: []};
  if (learning.schemaVersion !== LEARNING_VERSION) throw Error('learning_schema_invalid');
  refreshActiveMs(learning);
  return learning;
}
export function retainObservation(application, snapshot) {
  const learning = learningFor(application);
  const raw = snapshot.evidence;
  const observation = {schemaVersion: 1, id: randomUUID(), runId: application.runId,
    documentEpoch: snapshot.documentEpoch, surface:snapshot.loginRequired?'login':snapshot.activeDialog?'interaction':snapshot.fields?.length?'resume':snapshot.formStage==='detail'?'entry':'unknown', at: new Date().toISOString(),
    complete: raw?.schemaVersion === 1 && raw.complete === true,
    evidence: raw ? structuredClone(raw) : null};
  learning.observations.push(observation);
  learning.observations = learning.observations.slice(-20);
  application.observationId = observation.id;
  return observation;
}
export function settleEpisode(application, operation, verification) {
  const learning = learningFor(application), episode = learning.episodes.find(e => e.id === operation.episodeId);
  if (!episode || episode.verdict !== 'pending') return;
  episode.verdict = verification.status === 'passed' ? 'supported' : verification.reason === 'no_effect' ? 'no_progress' : verification.status;
  episode.verification = structuredClone(verification);
  episode.activeMs = Math.max(0, Number(operation.learningActiveMs ?? 0));
  episode.afterObservationId = application.observationId;
  episode.completedAt = new Date().toISOString();
  refreshActiveMs(learning);
}
export function learningSummary(application) {
  const l = application.learning;
  return l ? {schemaVersion:l.schemaVersion,mode:l.mode,activeMs:l.activeMs,episodes:l.episodes.length,
    supported:l.episodes.filter(e=>e.verdict==='supported').length,unresolved:l.unresolved.length,
    lastVerdict:l.episodes.at(-1)?.verdict ?? null} : null;
}
