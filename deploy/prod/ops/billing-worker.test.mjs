import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
const ops=path.dirname(fileURLToPath(import.meta.url));
function fixture(t,initial={}) {
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'worker-billing-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const bin=path.join(root,'bin');fs.mkdirSync(bin);
  const data=path.join(root,'cloud.json');fs.writeFileSync(data,JSON.stringify({instanceID:'i-own',instanceName:'worker-bot-trial',instanceStatus:'running',onDemand:true,calls:[],...initial}));
  fs.writeFileSync(path.join(bin,'ctyun-cli'),`#!/usr/bin/env node
const fs=require('fs'),a=process.argv.slice(2),op=a[1],val=k=>a[a.indexOf(k)+1],s=JSON.parse(fs.readFileSync(process.env.PROBE));
if(val('--regionID')!=='foshan')process.exit(9);
s.calls.push(op);let result={};let code=800;
if(op==='ListEcsInstances')result={results:[s]};
else if(op==='GetEcsInstanceDetails')result={...s};
else if(op==='StartEcsInstance')s.instanceStatus='running';
else if(op==='ShelveEcsInstance'){if(s.rejectShelve)code=900;else s.instanceStatus='shelve';}
else if(op==='ConvertEcsToCycle'){if(s.transportFailure){fs.writeFileSync(process.env.PROBE,JSON.stringify(s));process.exit(2);}if(s.rejectConvert)code=900;else {s.onDemand=false;s.expiredTime='2030-01-01T00:00:00Z';}}
else if(op==='UpdateEcsAutoRenewConfig')s.autoRenewStatus=0;
else if(op==='GetEcsAutoRenewConfig')result={autoRenewStatus:s.autoRenewStatus};
else process.exit(8);
fs.writeFileSync(process.env.PROBE,JSON.stringify(s));process.stdout.write(JSON.stringify({statusCode:code,errorCode:code===900?'TEST.REJECT':'',returnObj:result}));
`,{mode:0o755});
  const env={...process.env,PATH:bin+':'+process.env.PATH,PROBE:data,CTYUN_WORKER_STATE_ROOT:root,CTYUN_WORKER_REGION_ID:'foshan',CTYUN_WORKER_PROJECT_ID:'own',CATSCO_WORKER_DEPLOYMENT_JSON:''};
  return {root,read:()=>JSON.parse(fs.readFileSync(data)),run:action=>spawnSync('bash',[path.join(ops,'billing-worker.sh'),'--name','bot-trial','--action',action],{encoding:'utf8',env})};
}
test('saving stop confirms shelve and never claims residual costs stopped',{skip:process.platform==='win32'},t=>{
  const f=fixture(t);const r=f.run('suspend');assert.equal(r.status,0,r.stderr);
  assert.equal(f.read().instanceStatus,'shelve');assert.equal(JSON.parse(r.stdout).residual_resources_billable,true);
  assert.equal(f.run('suspend').status,0);assert.equal(f.read().calls.filter(x=>x==='ShelveEcsInstance').length,1);
});
test('unsupported saving stop fails without ordinary-stop fallback',{skip:process.platform==='win32'},t=>{
  const f=fixture(t,{rejectShelve:true});assert.notEqual(f.run('suspend').status,0);assert.equal(f.read().instanceStatus,'running');assert.ok(!f.read().calls.includes('StopEcsInstance'));
});
test('monthly instance cannot enter trial saving stop',{skip:process.platform==='win32'},t=>{
  const f=fixture(t,{onDemand:false});assert.notEqual(f.run('suspend').status,0);assert.ok(!f.read().calls.includes('ShelveEcsInstance'));
});
test('conversion preserves identity, resumes data and verifies monthly expiry and auto-renew',{skip:process.platform==='win32'},t=>{
  const f=fixture(t,{instanceStatus:'shelve'});const r=f.run('convert');assert.equal(r.status,0,r.stderr);
  assert.equal(JSON.parse(r.stdout).auto_renew_disabled,true);assert.equal(f.read().instanceID,'i-own');assert.equal(f.read().instanceStatus,'running');
  assert.equal(f.run('convert').status,0);assert.equal(f.read().calls.filter(x=>x==='ConvertEcsToCycle').length,1);
});
test('ambiguous conversion is retained and never blindly submitted twice',{skip:process.platform==='win32'},t=>{
  const f=fixture(t,{transportFailure:true});assert.notEqual(f.run('convert').status,0);const retry=f.run('convert');assert.notEqual(retry.status,0);assert.match(retry.stderr,/result unknown/);assert.equal(f.read().calls.filter(x=>x==='ConvertEcsToCycle').length,1);
});
test('definite conversion rejection reports failure and clears the pending marker',{skip:process.platform==='win32'},t=>{
  const f=fixture(t,{rejectConvert:true});assert.notEqual(f.run('convert').status,0);assert.equal(fs.existsSync(path.join(f.root,'bot-trial','conversion-requested')),false);assert.equal(f.read().onDemand,true);
  assert.equal(f.run('cancel').status,0);
});
test('conversion cancellation cannot discard an ambiguous order',{skip:process.platform==='win32'},t=>{
  const f=fixture(t,{transportFailure:true});assert.notEqual(f.run('convert').status,0);assert.notEqual(f.run('cancel').status,0);assert.ok(fs.existsSync(path.join(f.root,'bot-trial','conversion-requested')));
});
