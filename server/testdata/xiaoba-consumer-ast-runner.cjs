// Read-only consumer test runner. Executes actual XiaoBa functions extracted from TypeScript AST.
// Usage: node runner.cjs FIXTURE.json [XIAOBA_ROOT]
// Fixtures: [{name,pipeline:'live'|'cloud',message,expect:[],forbid:[],expectedFiles:[]}]
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const root=process.argv[3]||process.env.XIAOBA_ROOT;if(!root){console.error('XIAOBA_ROOT must point at the XiaoBa-CLI checkout');process.exit(2);}
const ts=require(root+'/node_modules/typescript/lib/typescript.js');
function functionsOf(file,names){const text=fs.readFileSync(file,'utf8'),tree=ts.createSourceFile(file,text,ts.ScriptTarget.Latest,true);const found={};function visit(n){if((ts.isFunctionDeclaration(n)||ts.isMethodDeclaration(n))&&names.includes(n.name?.getText(tree)))found[n.name.getText(tree)]=n.getText(tree);ts.forEachChild(n,visit);}visit(tree);for(const name of names)assert(found[name],'missing actual AST '+name);return found;}
const live=functionsOf(root+'/src/catscompany/index.ts',['parseMessage','isCatsCoAttachmentSummaryText','escapeRegExp']);
const cloud=functionsOf(root+'/src/catscompany/cloud-session-restore.ts',['cloudMessageText','cloudContentBlocksText']);
const sandbox={exports:{},Logger:{info(){}},createCatsCoMessageEnvelope:x=>x,createExecutionScope:()=>({}),extractCatsCoRuntimeContext:()=>undefined,extractCatsCoDeviceGrants:()=>[],extractCatsCoDeviceSelection:()=>undefined};
const code=live.escapeRegExp+'\n'+live.isCatsCoAttachmentSummaryText+'\nclass ParseOnly{'+live.parseMessage+'}\n'+Object.values(cloud).join('\n')+'\nexports.live=(ctx)=>ParseOnly.prototype.parseMessage.call({botUid:"usr9"},ctx);exports.cloud=cloudMessageText;';
vm.runInNewContext(ts.transpileModule(code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText,sandbox);
const fixtures=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));const results=[];
for(const f of fixtures){const m=f.message||{},p=f.pipeline||'cloud';let actual,result;try{if(p==='live'){result=sandbox.exports.live({...m,text:typeof m.content==='string'?m.content:'',topic:m.topic||m.topic_id||'p2p_7_9',senderId:m.from||'usr7',seq:m.seq||m.seq_id||1,isGroup:(m.topic||m.topic_id||'').startsWith('grp_')});actual=result?.text||'';}else actual=sandbox.exports.cloud(m);const missing=(f.expect||[]).filter(v=>!actual.includes(v)),forbidden=(f.forbid||[]).filter(v=>actual.includes(v));for(const file of f.expectedFiles||[])assert(result?.files?.some(v=>v.url===file),'attachment lost '+file);assert(missing.length===0&&forbidden.length===0,JSON.stringify({missing,forbidden,actual}));results.push({name:f.name,pipeline:p,status:'PASS'});}catch(e){results.push({name:f.name,pipeline:p,status:'FAIL',evidence:e.message});}}
console.log(JSON.stringify({passed:results.filter(v=>v.status==='PASS').length,failed:results.filter(v=>v.status==='FAIL').length,results},null,2));process.exitCode=results.some(v=>v.status==='FAIL')?1:0;
