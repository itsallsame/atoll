// Browser evidence primitives shared by the isolated observer and MAIN activator.
(() => {
  const interactive = 'button,a[href],input,select,textarea,[role="button"],[role="combobox"],[role="tab"]';
  const ignored = el => el.closest?.('#__cvmax-progress,script,style,noscript,[aria-hidden="true"],[inert]');
  const visible = el => !ignored(el) && !!el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden' && getComputedStyle(el).display !== 'none';
  const clean = value => String(value ?? '').replace(/\s+/g,' ').trim();
  const rect = el => {const r=el.getBoundingClientRect();return {left:r.left,top:r.top,width:r.width,height:r.height};};
  const topHit = el => {
    const r=el.getBoundingClientRect();
    if(r.bottom<=0||r.top>=innerHeight||r.right<=0||r.left>=innerWidth)return false;
    const hit=document.elementsFromPoint(Math.max(0,Math.min(innerWidth-1,r.left+r.width/2)),Math.max(0,Math.min(innerHeight-1,r.top+r.height/2))).find(x=>!ignored(x));
    return !!hit&&(el===hit||el.contains(hit));
  };
  const actionable = (el,scope=document) => {
    if(!visible(el)||scope!==document&&!scope.contains(el)||el.closest?.('[aria-hidden="true"],[inert]'))return false;
    const r=el.getBoundingClientRect();
    if(r.bottom<=0||r.top>=innerHeight||r.right<=0||r.left>=innerWidth)return r.width>0&&r.height>0;
    const hit=document.elementsFromPoint(Math.max(0,Math.min(innerWidth-1,r.left+r.width/2)),Math.max(0,Math.min(innerHeight-1,r.top+r.height/2))).find(x=>!ignored(x));
    return !hit||el===hit||el.contains(hit)||hit.contains?.(el);
  };
  const interactionSelector='button,input[type="button"],input[type="reset"],a[href],[role="button"],[role="tab"],[role="menuitem"]';
  const interactionCandidates = (scope=document) => {
    const all=[...scope.querySelectorAll(interactionSelector)].filter(el=>!el.disabled&&actionable(el,scope));
    return all.filter(candidate=>!all.some(other=>other!==candidate&&candidate.contains(other)));
  };
  const interactionIdentity = element => ({
    label:clean(element.textContent||element.value||element.getAttribute('aria-label')).toLowerCase(),
    role:clean(element.getAttribute('role')||(element.tagName==='A'?'link':element.tagName==='BUTTON'?'button':element.type||'control')).toLowerCase(),
    tag:element.tagName.toLowerCase(),
    type:clean(element.getAttribute('type')).toLowerCase(),
  });
  const resolveInteraction = (scope,expected) => interactionCandidates(scope).filter(element=>{
    const identity=interactionIdentity(element);
    return identity.label===expected.label&&identity.role===expected.role&&identity.tag===expected.tag&&identity.type===expected.type;
  })[expected.ordinal]??null;
  function surfaces() {
    const roots=new Set(document.querySelectorAll('[role="dialog"],[aria-modal="true"],dialog[open]'));
    const inspectedAncestors=new Set();
    // Structural evidence: positioned ancestors of actual controls, not company CSS names.
    for(const control of [...document.querySelectorAll(interactive)].slice(0,1200)){
      if(!visible(control))continue;
      for(let el=control.parentElement,depth=0;el&&el!==document.body&&depth<10;el=el.parentElement,depth++){
        if(inspectedAncestors.has(el))continue;inspectedAncestors.add(el);
        const style=getComputedStyle(el),r=el.getBoundingClientRect();
        if(!['fixed','absolute'].includes(style.position)||r.width<100||r.height<50||!visible(el))continue;
        const controls=[...el.querySelectorAll(interactive)].filter(visible);
        const raised=Number.parseInt(style.zIndex)>0;
        const focused=el.contains(document.activeElement)&&document.activeElement!==document.body;
        const viewport=style.position==='fixed';
        let layeredAncestor=false;for(let parent=el.parentElement,d=0;parent&&parent!==document.body&&d<6;parent=parent.parentElement,d++)if(getComputedStyle(parent).position==='fixed'&&Number.parseInt(getComputedStyle(parent).zIndex)>0)layeredAncestor=true;
        if(controls.length&&controls.length<=80&&(raised||focused||viewport||layeredAncestor)&&controls.some(topHit))roots.add(el);
      }
    }
    const all=[...roots].filter(el=>visible(el)&&(topHit(el)||[...el.querySelectorAll(interactive)].some(control=>visible(control)&&topHit(control))));
    return all.filter(el=>!all.some(other=>other!==el&&el.contains(other)))
      .map(el=>{const explicit=el.matches('[role="dialog"],[aria-modal="true"],dialog[open]'),bounds=el.getBoundingClientRect(),controls=[...el.querySelectorAll(interactive)].filter(visible),navigationRail=!explicit&&bounds.width<innerWidth*.5&&bounds.height>=innerHeight*.5&&controls.length>=3&&controls.every(control=>control.matches('a[href],[role="tab"],[role="menuitem"]')),compactFloatingWidget=!explicit&&getComputedStyle(el).position==='fixed'&&bounds.width<=240&&bounds.height<=240&&controls.length<=3;return{element:el,explicit,
        blocking:explicit||!(el.closest('header,nav,[role="banner"],[role="navigation"]')||navigationRail||compactFloatingWidget||bounds.width>=innerWidth*.8&&bounds.height<innerHeight*.25),
        position:getComputedStyle(el).position,zIndex:Number.parseInt(getComputedStyle(el).zIndex)||0,
        focused:el.contains(document.activeElement),rect:rect(el)}})
      .sort((a,b)=>Number(b.explicit)-Number(a.explicit)||b.zIndex-a.zIndex||Number(b.focused)-Number(a.focused));
  }
  function activeSurface() {return surfaces().find(surface=>surface.blocking)?.element??null;}
  function capture(ref, scope=document) {
    const selected=[...scope.querySelectorAll(interactive+',h1,h2,h3,h4,legend,p,label,[role="heading"],[role="alert"],[role="status"]')].filter(visible);
    const related=new Set(selected);for(const control of selected.slice(0,400))for(let parent=control.parentElement,depth=0;parent&&parent!==document.body&&depth<5;parent=parent.parentElement,depth++){if(scope!==document&&!scope.contains(parent))break;if(visible(parent))related.add(parent)}
    const all=[...related];
    const nodes=all.slice(0,400).map(el=>({ref:ref(el),parentRef:el.parentElement?ref(el.parentElement):null,
      interactive:el.matches(interactive),tag:el.tagName.toLowerCase(),role:el.getAttribute('role')??'',type:el.getAttribute('type')??'',
      // No input values or arbitrary attributes. Text remains private task evidence.
      text:clean(el.tagName==='INPUT'||el.tagName==='TEXTAREA'?'':el.textContent).slice(0,180),
      rect:rect(el),hit:topHit(el),disabled:el.disabled===true}));
    const layers=surfaces().slice(0,12).map(s=>({ref:ref(s.element),explicit:s.explicit,blocking:s.blocking,position:s.position,zIndex:s.zIndex,focused:s.focused,rect:s.rect,
      controls:[...s.element.querySelectorAll(interactive)].filter(visible).slice(0,80).map(ref)}));
    const structure=nodes.filter(n=>n.interactive&&n.hit&&!n.disabled).map(n=>[n.tag,n.role,n.type,n.text]);
    const embedded=[...scope.querySelectorAll('iframe')],visibleFrames=embedded.filter(visible).length,shadowHosts=[...scope.querySelectorAll('*')].filter(el=>!!el.shadowRoot&&!ignored(el)),shadowFieldControls=shadowHosts.flatMap(host=>[...host.shadowRoot.querySelectorAll('input,select,textarea,[role="combobox"]')]).filter(visible).length;
    return {schemaVersion:1,complete:all.length<=400&&embedded.length===0,fieldInventoryComplete:visibleFrames===0&&shadowFieldControls===0,visibleFrames,openShadowRoots:shadowHosts.length,shadowFieldControls,scope:scope===document?'document':ref(scope),nodes,layers,
      inaccessibleFrames:document.querySelectorAll('iframe').length,
      focusRef:document.activeElement&&document.activeElement!==document.body?ref(document.activeElement):null,
      scrollLocked:['hidden','clip'].includes(getComputedStyle(document.body).overflow),
      interactionDigest:JSON.stringify(structure)};
  }
  globalThis.CvMaxEvidence={capture,activeSurface,surfaces,topHit,actionable,interactionCandidates,interactionIdentity,resolveInteraction};
})();
