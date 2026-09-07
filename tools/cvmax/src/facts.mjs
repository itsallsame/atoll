const ID=/^[a-z][a-z0-9_.-]{0,99}$/;
const SOURCE=/^[a-z][a-z0-9+.-]*:.{1,500}$/i;

export function prepareFacts(records=[]){
 if(!Array.isArray(records)||records.length>200)throw Error('facts_invalid');
 const byId=new Map(),missing=[],conflicts=[];
 for(const record of records){
  if(!record||!ID.test(record.id??''))throw Error('fact_id_invalid');
  const value=typeof record.value==='string'?record.value.replace(/\r\n/g,'\n').trim():'';
  if(record.uncertain===true||!value){missing.push({id:record.id,sourceRef:record.sourceRef??null});continue}
  if(value.length>10_000||!SOURCE.test(record.sourceRef??''))throw Error('fact_value_or_source_invalid');
  const fact={id:record.id,value,sourceRef:record.sourceRef};
  const existing=byId.get(record.id);
  if(existing&&existing.value!==value){byId.delete(record.id);conflicts.push({id:record.id,sources:[existing.sourceRef,record.sourceRef]});continue}
  if(!conflicts.some(item=>item.id===record.id))byId.set(record.id,fact);
 }
 return{facts:[...byId.values()],missing,conflicts};
}

export function requireAuthorizedFacts(records=[]){
 const result=prepareFacts(records);
 if(result.missing.length||result.conflicts.length)throw Object.assign(Error('facts_missing_or_conflicting'),{detail:result});
 return result.facts;
}
