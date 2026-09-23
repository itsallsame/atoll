// Counters are owned by the application's durable task transaction. Never reset on reconnect.
export function newBudget(obligationCount=0){const count=Number.isSafeInteger(obligationCount)&&obligationCount>0?obligationCount:0;return {schemaVersion:2,actions:0,replans:{},strategies:{},activeMs:0,activeSince:null,limits:{actions:Math.max(100,Math.min(400,count*2+30)),replansPerPage:3,strategiesPerField:2,activeMs:600000}}}
export function charge(b,{page,field,action=false,replan=false,now=Date.now()}={}){
 if(b.schemaVersion!==2)throw Error('budget_schema_invalid');
 const elapsed=b.activeMs+(b.activeSince===null?0:Math.max(0,now-b.activeSince));
 if(elapsed>=b.limits.activeMs||action&&b.actions>=b.limits.actions||replan&&(b.replans[page]??0)>=b.limits.replansPerPage||field&&(b.strategies[field]??0)>=b.limits.strategiesPerField)throw Error('budget_exhausted');
 if(action)b.actions++;if(replan)b.replans[page]=(b.replans[page]??0)+1;if(field)b.strategies[field]=(b.strategies[field]??0)+1;
 b.activeMs=elapsed;b.activeSince=now;
}
export function suspendBudget(b,now=Date.now()){if(b.activeSince!==null)b.activeMs+=Math.max(0,now-b.activeSince);b.activeSince=null;}
