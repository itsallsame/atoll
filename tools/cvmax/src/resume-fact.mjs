const RESUME_ID=/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$/;

export function requireResumeName(value){
 const name=typeof value==='string'?value.trim():'';
 if(!name||name.length>120)throw Error('resume_name_invalid');
 return name;
}

export function requireResumeRef(value){
 if(!value||!RESUME_ID.test(value.id??'')||!Number.isSafeInteger(value.revision)||value.revision<1)throw Error('resume_ref_invalid');
 return{id:value.id,revision:value.revision};
}

export function publicResume(resume){
 return{resumeId:resume.resumeId,name:resume.name,revision:resume.revision,factCount:resume.projection.facts.length,hasSourceDocument:!!resume.sourceDocument,sourceDocument:resume.sourceDocument?{mime:resume.sourceDocument.mime,size:resume.sourceDocument.size,sha256:resume.sourceDocument.sha256}:null,createdAt:resume.createdAt,updatedAt:resume.updatedAt};
}
