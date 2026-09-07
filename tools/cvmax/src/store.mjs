import fs from 'node:fs';import path from 'node:path';
export class Store {
 constructor(file,initial){this.file=file;fs.mkdirSync(path.dirname(file),{recursive:true,mode:0o700});this.data=fs.existsSync(file)?JSON.parse(fs.readFileSync(file,'utf8')):structuredClone(initial);this.write();}
 write(){const fd=fs.openSync(this.file+'.tmp','w',0o600);try{fs.writeFileSync(fd,JSON.stringify(this.data));fs.fsyncSync(fd)}finally{fs.closeSync(fd)}fs.renameSync(this.file+'.tmp',this.file);const dir=fs.openSync(path.dirname(this.file),'r');try{fs.fsyncSync(dir)}finally{fs.closeSync(dir)}}
 tx(fn){if(this.poisoned)throw Error('durability_uncertain_restart_required');const before=structuredClone(this.data);let committing=false;try{const result=fn(this.data);committing=true;this.write();return structuredClone(result)}catch(e){this.data=before;if(committing)this.poisoned=true;throw e}}
}
