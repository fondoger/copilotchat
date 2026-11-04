async function(kind, operation, input) {
  const claude = kind === "claude-web";
  const host = claude ? "claude.ai" : "chatgpt.com";
  if (window.top !== window || location.protocol !== "https:" || location.host !== host) {
    return operation === "ready" ? {ready:false} : {failure:401};
  }
  const key = "__copilotNativeWeb";
  const state = window[key] ||= {kind, controller:new AbortController()};
  if (state.kind !== kind) return {failure:502};
  const uuid = value => typeof value === "string" && /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/i.test(value);
  const fail = status => { throw {webStatus:status}; };
  const digest = async text => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new TextEncoder().encode(text)))).map(n=>n.toString(16).padStart(2,"0")).join("");
  const bytes = image => {
    const data=atob(image.data), out=new Uint8Array(data.length);
    for(let i=0;i<data.length;i++) out[i]=data.charCodeAt(i);
    return new File([out],image.name||"image",{type:image.mime});
  };
  const model = (id,name,vision,efforts=[],labels={}) => ({id,name,vision,vision_known:true,pdf_known:true,tools_known:true,native_search_known:true,reasoning_known:true,reasoning:efforts.length>0,thinking_efforts:efforts,thinking_labels:labels,endpoint:"/chat/completions"});
  const checkedJSON = async response => {
    if (!response.ok) fail(response.status);
    if (!(response.headers.get("content-type")||"").includes("application/json")) fail(502);
    return response.json();
  };
  const claudeBootstrap = () => {
    const raw=document.cookie.split(";").map(c=>c.trim()).find(c=>c.startsWith("lastActiveOrg="))?.slice(14);
    const organization=raw?decodeURIComponent(raw):"";
    if(!uuid(organization)) return null;
    const element=document.querySelector("main")||document.getElementById("root");
    let fiber=element?.[Object.keys(element).find(k=>k.startsWith("__reactFiber$"))];
    for(let n=0;fiber&&n<256;fiber=fiber.return,n++){
      const props=fiber.memoizedProps;
      for(const field of ["client","value","queryClient"]){
        const client=props?.[field];
        if(typeof client?.getQueryCache!=="function")continue;
        const query=client.getQueryCache().getAll().find(q=>q.queryKey?.[0]==="current_account"&&q.queryKey.includes(organization)&&q.state.data?.account&&q.state.data?.model_selector_config);
        if(query)return {organization,bootstrap:query.state.data};
      }
    }
    return null;
  };
  const claudeInfo = async () => {
    const cached=claudeBootstrap();if(!cached)fail(401);
    const {organization,bootstrap}=cached;
    const account=bootstrap.account;
    if (!uuid(account?.uuid)||!account.memberships?.some(m=>m.organization?.uuid===organization)) fail(401);
    const config=bootstrap.model_selector_config?.find(c=>c.id==="chat");
    if (!config?.models) fail(502);
    state.organization=organization;
    state.modelConfig=new Map(config.models.filter(m=>!m.disabled&&!m.disabled_reason&&m.section!=="deprecated").map(m=>[m.id,m]));
    state.selections=bootstrap.model_selector_state?.find(s=>s.id==="chat");
    return {account:{id:await digest(kind+"\0"+organization+"\0"+account.uuid),login:"web",host},models:[...state.modelConfig.values()].map(m=>{
      const efforts=(m.thinking?.effort_options||[]).map(e=>e.id);
      if(m.thinking?.mode_options?.some(m=>m.id==="off")) efforts.unshift("off");
      const labels=Object.fromEntries((m.thinking?.effort_options||[]).map(e=>[e.id,e.name]));
      labels.off="Off";
      return model(m.id,m.name,!!m.capabilities?.mm_images,efforts,labels);
    })};
  };
  const chatGPTSDK = async () => {
    if(state.sdk) return state.sdk;
    const root=window.__reactRouterManifest?.routes?.root?.module;
    if(typeof root!=="string") fail(502);
    const rootURL=new URL(root,location.href);
    if(rootURL.origin!==location.origin||!rootURL.pathname.startsWith("/cdn/assets/")) fail(502);
    const source=await fetch(rootURL.href,{signal:state.controller.signal}).then(r=>r.ok?r.text():fail(r.status));
    const match=source.match(/import\{__webpack_require__\s+as\s+\w+\}from["']([^"']+)["']/);
    if(!match) fail(502);
    const runtimeURL=new URL(match[1],rootURL);
    if(runtimeURL.origin!==location.origin||!runtimeURL.pathname.startsWith("/cdn/assets/")) fail(502);
    const require=(await import(runtimeURL.href)).__webpack_require__;
    const find=predicate=>{for(const entry of Object.values(require.c||{})){try{if(predicate(entry?.exports))return entry.exports;}catch{}}return null;};
    const bootstrap=find(e=>typeof e?.getClientBootstrap==="function");
    const request=find(e=>typeof e?.Request?.safeGet==="function")?.Request;
    const sender=find(e=>typeof e?.submitChatGPTCompletion==="function");
    const upload=find(e=>typeof e?.uploadChatGptConversationFile==="function");
    if(!bootstrap||!request||!sender||bootstrap.getClientBootstrap().authStatus!=="logged_in") fail(401);
    // These are the current site's scope bindings, not a replacement request client.
    const token=require("OWtl").a;
    const element=document.querySelector("main");
    let fiber=element?.[Object.keys(element).find(k=>k.startsWith("__reactFiber$"))], scope;
    for(let n=0;fiber&&n<256;fiber=fiber.return,n++){
      const chain=fiber.memoizedProps?.value, node=chain instanceof Map?chain.get(token.id):null;
      if(node){scope=require("fbKo").g(token,chain,node);break;}
    }
    if(!scope) fail(502);
    return state.sdk={require,request,sender,upload,scope};
  };
  const chatGPTInfo = async () => {
    const sdk=await chatGPTSDK(),identity=sdk.require("CWeU");
    const accountId=sdk.scope.get(identity.a),userId=sdk.scope.get(identity.b);
    if(typeof accountId!=="string"||typeof userId!=="string"||!accountId||!userId) fail(401);
    state.expectedIdentity={accountId,userId};
    const catalog=await sdk.request.safeGet("/models",{parameters:{query:{iim:false,include_icons:false,is_gizmo:false}}});
    const byId=new Map((catalog.models||[]).map(m=>[m.slug,m]));
    const models=new Map();
    for(const category of catalog.categories||[]){
      if(category.disabled||category.disabled_reason) continue;
      for(const id of [category.default_model,...category.supported_models||[]]){
        const m=byId.get(id);if(!m||models.has(id))continue;
        const efforts=(m.thinking_efforts||[]).map(e=>e.thinking_effort);
        models.set(id,model(id,category.human_category_name||m.title,category.supported_features?.includes("image")||false,efforts,Object.fromEntries((m.thinking_efforts||[]).map(e=>[e.thinking_effort,e.full_label]))));
      }
    }
    if(!models.size) fail(502);
    state.modelConfig=byId;
    return {account:{id:await digest(kind+"\0"+accountId+"\0"+userId),login:"web",host},models:[...models.values()]};
  };
  const startClaude = async data => {
    const id=data.conversation_id||crypto.randomUUID();
    if(!uuid(id)||data.parent_id&&!uuid(data.parent_id)) fail(502);
    const config=state.modelConfig.get(data.model);if(!config)fail(403);
    const files=[];
    for(const image of data.images||[]){
      const form=new FormData();form.append("file",bytes(image));
      const uploaded=await checkedJSON(await fetch("/api/organizations/"+state.organization+"/conversations/"+id+"/wiggle/upload-file",{method:"POST",credentials:"include",body:form,signal:state.controller.signal}));
      if(!uuid(uploaded.file_uuid)||uploaded.file_kind!=="image")fail(502);
      files.push(uploaded.file_uuid);
    }
    const selected=state.selections?.thinking_by_model?.find(m=>m.id===data.model)?.thinking;
    const modes=(config.thinking?.mode_options||[]).map(m=>m.id);
    const selectedMode=modes.includes(selected?.mode)?selected.mode:modes[0];
    let mode=data.effort==="off"?"off":data.effort?modes.find(m=>m!=="off"):selectedMode;
    const options=config.thinking?.effort_options||[];
    const effort=data.effort&&data.effort!=="off"?data.effort:selected?.effort||options.find(e=>e.recommended)?.id||options[0]?.id;
    const body={prompt:data.prompt,model:data.model,rendering_mode:"messages",attachments:[],files,sync_sources:[],locale:navigator.language||"en-US",timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,completion_request_id:crypto.randomUUID(),turn_message_uuids:{human_message_uuid:crypto.randomUUID(),assistant_message_uuid:crypto.randomUUID()},tools:[]};
    if(mode)body.thinking_mode=mode;
    if(effort)body.effort=effort;
    if(data.parent_id)body.parent_message_uuid=data.parent_id;
    if(!data.conversation_id)body.create_conversation_params={name:"",model:data.model,include_conversation_preferences:true,is_temporary:false};
    state.conversation=id;
    const response=await fetch("/api/organizations/"+state.organization+"/chat_conversations/"+id+"/completion",{method:"POST",credentials:"include",headers:{"Content-Type":"application/json",Accept:"text/event-stream"},body:JSON.stringify(body),signal:state.controller.signal});
    state.reader=response.body.getReader();
    return {status:response.status,contentType:response.headers.get("content-type")||"",conversation:id};
  };
  const startChatGPT = async data => {
    const sdk=await chatGPTSDK(),attachments=[];
    for(const image of data.images||[]){
      if(!sdk.upload)fail(502);
      const file=bytes(image),bitmap=await createImageBitmap(file);
      const {width,height}=bitmap;bitmap.close();
      const attachment=await sdk.upload.uploadChatGptConversationFile(sdk.scope,file,{model:state.modelConfig.get(data.model),storeInLibrary:false,signal:state.controller.signal,expectedIdentity:state.expectedIdentity});
      attachments.push({...attachment,width,height});
    }
    let accept,reject;
    const captured=new Promise((resolve,rejectPromise)=>{accept=resolve;reject=rejectPromise;});
    const original=window.fetch;
    const wrapped=async function(request,init){
      const response=await original.apply(this,arguments);
      try{
        const url=new URL(typeof request==="string"?request:request.url,location.href);
        if(url.origin===location.origin&&url.pathname==="/backend-api/f/conversation"&&typeof init?.body==="string"){
          const sent=JSON.parse(init.body);
          if(!state.reader&&sent.messages?.some(m=>m.author?.role==="user"&&Array.isArray(m.content?.parts)&&m.content.parts.filter(p=>typeof p==="string").join("").trim()===data.prompt.trim())){
            state.reader=response.clone().body.getReader();
            accept({status:response.status,contentType:response.headers.get("content-type")||"",conversation:data.conversation_id||""});
          }
        }
      }catch{reject({webStatus:502});}
      return response;
    };
    window.fetch=wrapped;
    state.restore=()=>{if(window.fetch===wrapped)window.fetch=original;};
    state.controller.signal.addEventListener("abort",()=>reject({webStatus:502}),{once:true});
    sdk.sender.submitChatGPTCompletion(sdk.scope,{prompt:data.prompt,model:data.model,attachments,conversationId:data.conversation_id||undefined,parentMessageId:data.parent_id||undefined,thinkingEffort:data.effort||undefined,isDoNotRemember:true,expectedIdentity:state.expectedIdentity,startupSignal:state.controller.signal,onCompletion:()=>{if(!state.reader)reject({webStatus:502});},onServerThreadIdChange:()=>{}}).catch(reject);
    return captured;
  };
  try {
    switch(operation){
      case "ready":
        if(claude)return {ready:!!claudeBootstrap()};
        if(!window.__reactRouterManifest?.routes?.root?.module||!document.querySelector('[contenteditable="true"]'))return {ready:false};
        {
          const sdk=await chatGPTSDK(),identity=sdk.require("CWeU");
          return {ready:typeof sdk.scope.get(identity.a)==="string"&&typeof sdk.scope.get(identity.b)==="string"};
        }
      case "info":
        return state.info ||= await (claude?claudeInfo():chatGPTInfo());
      case "start":
        if(state.reader||!state.info||!state.info.models.some(m=>m.id===input.model))fail(502);
        state.input=input;
        return await (claude?startClaude(input):startChatGPT(input));
      case "read": {
        if(!state.reader)fail(502);
        let chunk;
        try{chunk=await state.reader.read();}catch{return {failed:true,done:true,data:""};}
        if(chunk.done){state.restore?.();return {done:true,data:""};}
        let raw="";for(let i=0;i<chunk.value.length;i+=8192)raw+=String.fromCharCode(...chunk.value.subarray(i,i+8192));
        return {done:false,data:btoa(raw)};
      }
      case "title": {
        if(!uuid(input.id))fail(502);
        if(claude){
          if(input.id!==state.conversation)fail(502);
          const response=await fetch("/api/organizations/"+state.organization+"/chat_conversations/"+input.id+"/title",{method:"POST",credentials:"include",headers:{"Content-Type":"application/json"},body:JSON.stringify({message_content:state.input.prompt,recent_titles:[]}),signal:state.controller.signal});
          const data=await checkedJSON(response);return {title:typeof data.title==="string"?data.title:""};
        }
        const sdk=await chatGPTSDK(),data=await sdk.request.safeGet("/conversation/"+input.id);
        return {title:typeof data.title==="string"?data.title:""};
      }
      case "abort":
        state.controller.abort();state.restore?.();
        if(state.reader)try{await state.reader.cancel();}catch{}
        return {};
    }
  } catch(error) {
    const status=error?.webStatus??error?.responseStatus;
    return {failure:[401,403,429].includes(status)?status:502};
  }
  return {failure:502};
}
