// Test-only AST runner: actual XiaoBa live parse -> actual downloadFile HTTP
// bytes -> actual buildMultimodalMessage + actual sharp/createImageBlock. No
// provider call and no client source changes. Separately checks WS replay.
const fs=require('node:fs'), path=require('node:path'), os=require('node:os'), vm=require('node:vm'), assert=require('node:assert/strict');
const root=process.argv[3]||process.env.XIAOBA_ROOT, fixture=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));
const ts=require(root+'/node_modules/typescript/lib/typescript.js');
function source(relative){return fs.readFileSync(path.join(root,relative),'utf8');}
function functions(relative,names){const text=source(relative),tree=ts.createSourceFile(relative,text,ts.ScriptTarget.Latest,true),out={};function visit(node){if((ts.isFunctionDeclaration(node)||ts.isMethodDeclaration(node))&&names.includes(node.name?.getText(tree)))out[node.name.getText(tree)]=node.getText(tree);ts.forEachChild(node,visit);}visit(tree);for(const name of names)assert(out[name],'actual function missing '+name);return out;}
function run(text,sandbox){vm.runInNewContext(ts.transpileModule(text,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022,esModuleInterop:true}}).outputText,sandbox);}
const imageModule={exports:{}};
run(source('src/utils/image-utils.ts'),{exports:imageModule.exports,module:imageModule,require:id=>id==='sharp'?require(root+'/node_modules/sharp'):require(id),console,Buffer});
const methods=functions('src/catscompany/index.ts',['parseMessage','isCatsCoAttachmentSummaryText','escapeRegExp','buildMultimodalMessage','formatAttachmentReferenceForModel']);
const download=functions('src/catscompany/message-sender.ts',['downloadFile']);
const Logger={info(){},warning(){},error(){}};
const sandbox={exports:{},require:id=>id==='../utils/image-utils'?imageModule.exports:require(id),Logger,ConfigManager:{getConfigReadonly:()=>({model:'offline-vision-test'})},resolvePrimaryModelVisionCapability:async()=> 'supported',formatPathForLog:x=>x,createCatsCoMessageEnvelope:x=>x,createExecutionScope:()=>({}),extractCatsCoRuntimeContext:()=>undefined,extractCatsCoDeviceGrants:()=>[],extractCatsCoDeviceSelection:()=>undefined,fs,path,fetch,Buffer,process,console};
run(methods.escapeRegExp+'\n'+methods.isCatsCoAttachmentSummaryText+'\nclass Consumer{'+methods.parseMessage+methods.buildMultimodalMessage+methods.formatAttachmentReferenceForModel+'}\nclass Sender{'+download.downloadFile+'}\nexports.Consumer=Consumer;exports.Sender=Sender;',sandbox);
(async()=>{
 const directory=fs.mkdtempSync(path.join(os.tmpdir(),'catsco-real-live-vision-'));
 try{
  for(const pipeline of ['live','history']){
   const data=fixture[pipeline];
   const consumer=new sandbox.exports.Consumer();consumer.botUid='usr9';
   const parsed=consumer.parseMessage({...data,text:data.content,topic:data.topic,senderId:data.from,seq:data.seq,isGroup:false});
   assert.equal(parsed.files.length,2,'actual parse must extract two images');
   assert(parsed.files.every(file=>file.type==='image'),'actual file types must remain image');
   const sender=new sandbox.exports.Sender();sender.baseUrl=fixture.platform_url;
   const attachments=[];
   for(const [i,file] of parsed.files.entries()){
    assert.match(file.url,/^\/uploads\/images\/\d{8}_[a-f0-9]{32}\.jpe?g$/);
    const localPath=await sender.downloadFile(file.url,file.fileName,{targetPath:path.join(directory,pipeline+i+'.jpg')});
    assert(localPath,'actual HTTP download failed');
    const bytes=fs.readFileSync(localPath);assert(bytes[0]===255&&bytes[1]===216,'download must contain JPEG pixels');
    attachments.push({type:'image',localPath,fileName:file.fileName});
   }
   const blocks=await consumer.buildMultimodalMessage(parsed.text,attachments);
   const images=blocks.filter(block=>block.type==='image');assert.equal(images.length,2,'model input needs two visual blocks');
   for(const block of images){assert.equal(block.source.type,'base64');assert.equal(block.source.media_type,'image/jpeg');assert(Buffer.from(block.source.data,'base64').length>50,'not a URL/text-only image');}
   assert(blocks.some(block=>block.type==='text'&&block.text.includes('Gateway 标注')),'annotation user text lost');
   console.log('PASS actual '+pipeline+' parse -> HTTP downloadFile -> sharp/createImageBlock -> 2 base64 vision blocks (offline provider input)');
  }
 }finally{fs.rmSync(directory,{recursive:true,force:true});}
})().catch(error=>{console.error(error.stack);process.exitCode=1;});
