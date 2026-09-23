const ms=value=>{const parsed=Date.parse(value??'');return Number.isFinite(parsed)?parsed:null};
const elapsed=(start,end)=>{const a=ms(start),b=ms(end);return a!=null&&b!=null&&b>=a?b-a:null};
export function applicationPerformance(task,application){
 const timing=application.performance??{},start=task.createdAt;
 return{openToVisibleMs:elapsed(timing.openStartedAt,timing.surfaceOpenedAt),openToCompleteMs:elapsed(timing.openStartedAt,application.completedAt),taskToFormMs:elapsed(start,timing.formReadyAt),formToFirstActionMs:elapsed(timing.formReadyAt,timing.firstActionAt),taskToFirstActionMs:elapsed(start,timing.firstActionAt),taskToCompleteMs:elapsed(start,application.completedAt),executionPath:timing.executionPath??null,agentTakeover:timing.agentTakeover===true,outcome:application.outcome};
}
export function anonymousMetric(task,application){
 const performance=applicationPerformance(task,application),recipeId=application.recipe?.recipeId??null;
 return{schemaVersion:1,event:'application_completed',...performance,recipeId:typeof recipeId==='string'&&/^[a-f0-9]{64}$/i.test(recipeId)?recipeId:null,factCountBucket:task.factSnapshot?.facts?.length?task.factSnapshot.facts.length<=10?'1-10':task.factSnapshot.facts.length<=30?'11-30':'31+':'0',at:application.completedAt};
}
export function performanceSummary(tasks){
 const rows=Object.values(tasks??{}).flatMap(task=>(task.applications??[]).filter(item=>item.completedAt).map(item=>applicationPerformance(task,item))),values=key=>rows.map(row=>row[key]).filter(Number.isFinite).sort((a,b)=>a-b),quantile=(items,p)=>items.length?items[Math.min(items.length-1,Math.floor((items.length-1)*p))]:null;
 const timings=Object.fromEntries(['openToVisibleMs','openToCompleteMs','taskToFormMs','formToFirstActionMs','taskToFirstActionMs','taskToCompleteMs'].map(key=>{const items=values(key);return[key,{samples:items.length,p50:quantile(items,.5),p95:quantile(items,.95)}]})),known=rows.filter(row=>row.executionPath==='recipe').map(row=>row.openToCompleteMs).filter(Number.isFinite).sort((a,b)=>a-b),p50=quantile(known,.5),p95=quantile(known,.95),requiredSamples=20;
 return{completedApplications:rows.length,recipeHits:known.length,agentTakeovers:rows.filter(row=>row.agentTakeover).length,timings,knownPageReleaseGate:{requiredSamples,samples:known.length,p50,p95,p50TargetMs:8000,p95TargetMs:20000,pass:known.length<requiredSamples?null:p50<=8000&&p95<=20000}};
}
