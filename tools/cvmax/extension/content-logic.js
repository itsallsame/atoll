(()=>{
 const desiredChecked=value=>/^(true|yes|1|是)$/i.test(String(value??'').trim());
 const nativeParseSignal=text=>/(解析|导入|一键|自动|智能).{0,12}(简历|履历)|(简历|履历).{0,12}(解析|导入|填充|填写|更新)|(parse|import|autofill).{0,12}(resume|cv)|(resume|cv).{0,12}(parse|import|autofill)/i.test(String(text??''));
 const nativeParseConfirmSignal=text=>/(开始|确认|立即|使用|覆盖).{0,10}(解析|导入|填充|填写)|(解析|导入|填充|填写).{0,10}(并覆盖|并填充|并填写)|(parse|import|autofill).{0,10}(now|resume|cv)/i.test(String(text??''));
 const nativeParseBusySignal=text=>/正在.{0,8}(解析|导入)|(解析|导入|处理)中|(parsing|importing|processing).{0,8}(resume|cv)?/i.test(String(text??''));
 const applicationStartSignal=text=>/^(立即申请|立即投递|申请职位|我要应聘|马上申请|应聘此职位|投递简历|apply now)$/i.test(String(text??'').replace(/\s+/g,' ').trim());
 const resumePrerequisiteSignal=text=>/^(创建完整简历|创建在线简历|完善简历|完善在线简历|补充简历|create (a )?(complete )?(resume|cv)|complete (your )?(resume|cv))$/i.test(String(text??'').replace(/\s+/g,' ').trim());
 const resumeEditSignal=text=>/^(编辑|编辑简历|完善|完善简历|edit|edit resume|complete resume)$/i.test(String(text??'').replace(/\s+/g,' ').trim());
 const dialogStateFromText=(text,{hasPassword=false,hasCaptcha=false}={})=>hasCaptcha?'captcha':hasPassword?'login':/(简历|履历).{0,24}(完整度|完善|创建|补充)|(完整度|完善|创建|补充).{0,24}(简历|履历)|(resume|cv).{0,24}(complete|create|profile)/i.test(String(text??''))?'resume_prerequisite':'unknown';
 const boundedDialogText=(text,max=600)=>String(text??'').replace(/\s+/g,' ').trim().slice(0,max);
 const finalSubmitSignal=text=>/(提交申请|确认投递|立即投递|完成申请|确认申请|submit application|complete application)/i.test(String(text??''));
 const equivalentStartControl=(candidates,textOf)=>{if(!candidates.length)return null;const labels=new Set(candidates.map(item=>String(textOf(item)??'').replace(/\s+/g,' ').trim().toLowerCase()));return labels.size===1?candidates[0]:null};
 const sectionNames=['基本信息','个人信息','简历信息','求职意向','教育经历','实习经历','工作经历','项目经历','作品','竞赛','证书','语言能力','自我评价','社交账号','确认与提交'];
 const sectionFromText=text=>sectionNames.find(name=>String(text??'').includes(name))??null;
 const sectionFromGroup=group=>({
  'formily-item-education_list':'教育经历','formily-item-internship_list':'实习经历','formily-item-career_list':'工作经历','formily-item-project_list':'项目经历','formily-item-works_list':'作品','formily-item-competition_list':'竞赛','formily-item-certificate_list':'证书','formily-item-language_list':'语言能力','formily-item-sns_list':'社交账号'
 })[group]??null;
 function sectionFor(element,clean){
  for(let node=element?.parentElement,depth=0;node&&depth<7;node=node.parentElement,depth++){
   const explicit=node.getAttribute?.('data-section')||node.getAttribute?.('aria-label');if(explicit){const section=sectionFromText(clean(explicit));if(section)return section}
   const heading=node.querySelector?.(':scope > h1,:scope > h2,:scope > h3,:scope > h4,:scope > [role="heading"],:scope > [class*="title"]');
   const fromHeading=sectionFromText(clean(heading?.textContent));if(fromHeading)return fromHeading;
   const fromText=sectionFromText(clean(node.textContent));if(fromText)return fromText;
  }
  return null;
 }
 function leafOptions(candidates){return candidates.filter(candidate=>!candidates.some(other=>other!==candidate&&candidate.contains(other)))}
 function nearestOption(anchor,candidates){if(candidates.length===1)return candidates[0];const a=anchor.getBoundingClientRect(),center=rect=>[rect.left+rect.width/2,rect.top+rect.height/2],ac=center(a),ranked=candidates.map(item=>{const point=center(item.getBoundingClientRect());return{item,distance:(point[0]-ac[0])**2+(point[1]-ac[1])**2}}).sort((x,y)=>x.distance-y.distance);return ranked.length&&(!ranked[1]||ranked[0].distance+1<ranked[1].distance)?ranked[0].item:null}
 function topmostOption(doc,candidates){if(!candidates.length)return null;for(const candidate of candidates){const rect=candidate.getBoundingClientRect(),hits=doc.elementsFromPoint(rect.left+rect.width/2,rect.top+rect.height/2);for(const hit of hits){const matches=candidates.filter(item=>item===hit||item.contains(hit)||hit.contains?.(item));if(matches.length){return matches.find(item=>item.getAttribute?.('role')==='option')??matches[0]}}}return null}
 function equivalentOption(candidates){if(candidates.length<2)return candidates[0]??null;const base=candidates[0].getBoundingClientRect(),same=candidates.every(item=>{const rect=item.getBoundingClientRect();return Math.abs(rect.left-base.left)<1&&Math.abs(rect.top-base.top)<1&&Math.abs(rect.width-base.width)<1&&Math.abs(rect.height-base.height)<1});return same?(candidates.find(item=>item.getAttribute?.('role')==='option')??candidates[0]):null}
 function matchingOptions(candidates,value,clean){const desired=clean(value),exact=candidates.filter(item=>clean(item.textContent)===desired);if(exact.length)return{candidates:exact,normalized:false,matchedValue:desired};const labels=[...new Set(candidates.map(item=>clean(item.textContent)).filter(label=>label.length>=2&&(desired.includes(label)||label.includes(desired))))];return labels.length===1?{candidates:candidates.filter(item=>clean(item.textContent)===labels[0]),normalized:true,matchedValue:labels[0]}:{candidates:[],normalized:false,matchedValue:null}}
 globalThis.CvMaxContentLogic=Object.freeze({desiredChecked,nativeParseSignal,nativeParseConfirmSignal,nativeParseBusySignal,applicationStartSignal,resumePrerequisiteSignal,resumeEditSignal,dialogStateFromText,boundedDialogText,finalSubmitSignal,equivalentStartControl,sectionFromText,sectionFromGroup,sectionFor,leafOptions,nearestOption,topmostOption,equivalentOption,matchingOptions});
})();
