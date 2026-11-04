package webapi

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPageBridgeUsesScopedWebsiteStateAndFixedOperations(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the page bridge harness")
	}
	models, err := os.ReadFile("testdata/claude-models.json")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := os.ReadFile("../../provider/webchat/testdata/claude.sse")
	if err != nil {
		t.Fatal(err)
	}
	const harness = `
const vm=require('node:vm'),assert=require('node:assert/strict');
const {webcrypto}=require('node:crypto'),{File}=require('node:buffer');
const {source,modelData,stream}=JSON.parse(require('node:fs').readFileSync(0,'utf8'));
const org='00000000-0000-4000-8000-000000000001',user='00000000-0000-4000-8000-000000000002',file='00000000-0000-4000-8000-000000000003';
function context(kind){
 const claude=kind==='claude-web',calls=[];
 const bootstrap={account:{uuid:user,email_address:'private@example.invalid',memberships:[{organization:{uuid:org}}]},...JSON.parse(modelData)};
 const query={queryKey:['current_account',org],state:{data:bootstrap}};
 const client={getQueryCache:()=>({getAll:()=>[query]})};
 const element={__reactFiber$test:{memoizedProps:{client},return:null}};
 const c={location:{href:claude?'https://claude.ai/new':'https://chatgpt.com/',protocol:'https:',host:claude?'claude.ai':'chatgpt.com',origin:claude?'https://claude.ai':'https://chatgpt.com'},document:{cookie:'lastActiveOrg='+org,querySelector:()=>element,getElementById:()=>element},crypto:webcrypto,TextEncoder,Uint8Array,AbortController,File,FormData,atob,btoa,URL,Map,Intl,navigator:{language:'en-US'}};
 c.window=c;c.top=c;
 c.fetch=async (url,options={})=>{
  calls.push({url,options});
  if(String(url).endsWith('/wiggle/upload-file')){
   assert.deepEqual([...options.body.keys()],['file']);assert.equal(options.body.get('file').type,'image/png');
   return Response.json({file_uuid:file,file_kind:'image'});
  }
  if(String(url).endsWith('/completion'))return new Response(stream,{headers:{'content-type':'text/event-stream'}});
  if(String(url).endsWith('/title'))return Response.json({title:'Synthetic image'});
  if(String(url).endsWith('/f/conversation'))return new Response('blocked',{status:403,headers:{'content-type':'text/html'}});
  throw Error('unexpected network operation');
 };
 vm.createContext(c);const call=vm.runInContext('('+source+')',c);
 return {c,calls,query,call:(op,input=null)=>call(kind,op,input)};
}
(async()=>{
 const t=context('claude-web');
 assert.equal((await t.call('ready')).ready,true);
 const info=await t.call('info');
 assert.equal(t.calls.length,0,'model discovery duplicated website bootstrap');
 assert.equal(info.models.length,4);
 assert(info.models.every(m=>!m.id.includes('opus')&&!m.id.includes('fable')),'locked models escaped filtering');
 assert(!JSON.stringify(info).includes(user)&&!JSON.stringify(info).includes('private@example'),'identity leaked instead of a scope binding');
 assert(info.models.find(m=>m.id==='claude-sonnet-5-5').thinking_efforts.includes('high'));
 assert(!info.models.find(m=>m.id==='claude-sonnet-5-5').thinking_efforts.includes('off'));
 const started=await t.call('start',{model:'claude-sonnet-5-5',prompt:'synthetic',effort:'high',images:[{name:'test.png',mime:'image/png',data:'AQ=='}]});
 assert.equal(started.status,200);assert.equal(started.conversation.length,36);
 const body=JSON.parse(t.calls.find(x=>x.url.endsWith('/completion')).options.body);
 assert.deepEqual(body.files,[file]);assert.equal(body.thinking_mode,'auto');assert.equal(body.effort,'high');assert.equal(body.create_conversation_params.is_temporary,false);
 assert(t.calls[0].url.includes(started.conversation),'upload was not bound to the new conversation');
 let text='';for(;;){const part=await t.call('read');text+=Buffer.from(part.data||'','base64').toString('utf8');if(part.done)break;}
 assert.equal(text,stream);
 assert.equal((await t.call('title',{id:started.conversation})).title,'Synthetic image');
 const n=t.calls.length;assert.equal((await t.call('title',{id:'not-a-conversation'})).failure,502);assert.equal(t.calls.length,n);
 await t.call('abort');assert.equal(t.c.__copilotNativeWeb.controller.signal.aborted,true);
 const wrong=context('claude-web');wrong.query.queryKey[1]=file;
 assert.equal((await wrong.call('ready')).ready,false);assert.equal((await wrong.call('info')).failure,401);assert.equal(wrong.calls.length,0);
 const untrusted=context('claude-web');untrusted.c.location.host='attacker.invalid';assert.equal((await untrusted.call('info')).failure,401);assert.equal(untrusted.calls.length,0);
 const g=context('chatgpt-web');let submitted=0;
 g.c.__copilotNativeWeb={kind:'chatgpt-web',controller:new AbortController(),sdk:{
  require:()=>({a:'account',b:'user'}),scope:{get:key=>key==='account'?org:user},
  request:{safeGet:async path=>{assert.equal(path,'/models');return {models:[{slug:'auto',title:'Auto'}],categories:[{default_model:'auto',human_category_name:'Auto',supported_features:[]}],default_model_slug:'auto'};}},
  sender:{submitChatGPTCompletion:async(scope,options)=>{submitted++;assert.equal(options.expectedIdentity.accountId,org);assert.equal(options.isDoNotRemember,true);setTimeout(()=>{g.c.fetch('/backend-api/f/conversation',{method:'POST',body:JSON.stringify({messages:[{author:{role:'user'},content:{parts:[options.prompt.trim()]}}]})}).then(()=>options.onCompletion());},0);return {serverConversationId:null};}}
 }};
 await g.call('info');const rejected=await g.call('start',{model:'auto',prompt:' synthetic '});
 assert.equal(submitted,1);assert.equal(rejected.status,403,'SDK completion callback was mistaken for a successful answer');
 await g.call('abort');
 for(const status of [401,403,429,500]){
  const failed=context('chatgpt-web');
  failed.c.__copilotNativeWeb={kind:'chatgpt-web',controller:new AbortController(),sdk:{...g.c.__copilotNativeWeb.sdk,sender:{submitChatGPTCompletion:async()=>{throw {responseStatus:status,message:'private upstream details'};}}}};
  await failed.call('info');const failure=await failed.call('start',{model:'auto',prompt:'synthetic'});
  assert.equal(failure.failure,status===500?502:status,'SDK HTTP rejection lost its classification');
  assert(!JSON.stringify(failure).includes('private'),'SDK error details crossed the native bridge');
  await failed.call('abort');
 }
 process.stdout.write('scoped bootstrap, images, streaming, rejection and origin guards passed');
})().catch(e=>{console.error(e);process.exit(1)});
`
	input, err := json.Marshal(map[string]string{"source": pageScript, "modelData": string(models), "stream": string(stream)})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "-e", harness)
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("page bridge harness: %v\n%s", err, output)
	}
}
