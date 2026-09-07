import fs from 'node:fs';
import path from 'node:path';
import {createHash,randomUUID} from 'node:crypto';
// One application instance is configured for one existing Atoll channel workspace.
// No caller can choose the channel root, device or identity.
export class Attachments {
 constructor({root,resourcePrefix,maxBytes=10*1024*1024}){this.root=fs.realpathSync(root);this.prefix=resourcePrefix;this.maxBytes=maxBytes;}
 read({resource,sha256,size,mime,expiresAt,scope}){
  if(typeof resource!=='string'||!resource.startsWith(this.prefix))throw Error('attachment_scope_mismatch');
  const saved=scope==='resume';
  if(!Number.isSafeInteger(size)||size<1||size>this.maxBytes||(saved?expiresAt!=null:!Number.isFinite(expiresAt)||Date.now()>expiresAt))throw Error('attachment_expired_or_size_invalid');
  const suffix=resource.slice(this.prefix.length);let relative;try{relative=decodeURIComponent(suffix)}catch{throw Error('attachment_path_invalid')}
  if(!relative||path.isAbsolute(relative)||relative.split(/[\\/]/).some(x=>!x||x==='.'||x==='..')||relative.includes('\0')||saved&&!relative.startsWith('resumes/'))throw Error('attachment_path_invalid');
  const file=fs.realpathSync(path.join(this.root,relative));if(!file.startsWith(this.root+path.sep))throw Error('attachment_outside_workspace');
  const fd=fs.openSync(file,fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW);let bytes;
  try{const stat=fs.fstatSync(fd);if(!stat.isFile()||stat.size!==size||stat.size>this.maxBytes)throw Error('attachment_size_changed');bytes=fs.readFileSync(fd);}finally{fs.closeSync(fd)}
  if(bytes.length!==size||createHash('sha256').update(bytes).digest('hex')!==sha256)throw Error('attachment_hash_changed');
  const ext=path.extname(relative).toLowerCase();
  const valid=mime==='text/plain'&&ext==='.txt'&&!bytes.includes(0)||mime==='application/pdf'&&ext==='.pdf'&&bytes.subarray(0,5).toString()==='%PDF-'||mime==='application/vnd.openxmlformats-officedocument.wordprocessingml.document'&&ext==='.docx'&&bytes[0]===80&&bytes[1]===75;
  if(!valid)throw Error('attachment_type_unsupported');
  return {attachmentId:randomUUID(),resource,sha256,size,mime,filename:path.basename(relative),expiresAt,scope,bytes};
 }
 persistResume(ref,resumeId,revision){
  const source=this.read(ref),ext=source.mime==='application/pdf'?'.pdf':source.mime==='application/vnd.openxmlformats-officedocument.wordprocessingml.document'?'.docx':'.txt',relative=path.posix.join('resumes',resumeId,`${revision}${ext}`),destination=path.join(this.root,...relative.split('/'));
  fs.mkdirSync(path.dirname(destination),{recursive:true,mode:0o700});
  try{const fd=fs.openSync(destination,'wx',0o600);try{fs.writeFileSync(fd,source.bytes);fs.fsyncSync(fd)}finally{fs.closeSync(fd)}}catch(error){if(error.code!=='EEXIST')throw error;const existing=fs.readFileSync(destination);if(existing.length!==source.bytes.length||createHash('sha256').update(existing).digest('hex')!==source.sha256)throw Error('resume_source_conflict')}
  return{resource:this.prefix+encodeURIComponent(relative),sha256:source.sha256,size:source.bytes.length,mime:source.mime,scope:'resume'};
 }
}
