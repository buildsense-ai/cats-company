import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
const ops = path.dirname(fileURLToPath(import.meta.url));
const publicProfile = {profile:'public_ip',env:{CTYUN_WORKER_REGION_ID:'foshan',CTYUN_WORKER_PROJECT_ID:'enterprise',CTYUN_JUMP_IP:''}};

test('tenant profile persists, isolates SSH and selects the public address', {skip:process.platform==='win32'}, t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(),'worker-profile-')); t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const script = `source "$OPS/worker-deployment-profile.sh"
worker_profile_load bot-test 1
unset CATSCO_WORKER_DEPLOYMENT_JSON
export CTYUN_WORKER_REGION_ID=wrong
worker_profile_load bot-test
printf '%s|%s|%s|' "$CTYUN_WORKER_REGION_ID" "$CTYUN_JUMP_IP" "$CATSCO_ARTIFACT_GATEWAY_SSH_IP"
worker_connection_ip <<<'{"fixedIPList":["172.27.7.2"],"floatingIP":"8.8.8.8"}'`;
  const result=spawnSync('bash',['-eu','-c',script],{encoding:'utf8',env:{...process.env,OPS:ops,CTYUN_WORKER_STATE_ROOT:root,CTYUN_JUMP_IP:'original-gateway',CATSCO_WORKER_DEPLOYMENT_JSON:JSON.stringify(publicProfile)}});
  assert.equal(result.status,0,result.stderr); assert.equal(result.stdout.trim(),'foshan||original-gateway|8.8.8.8');
  assert.equal(JSON.parse(fs.readFileSync(path.join(root,'bot-test/deployment.json'))).profile,'public_ip');
});

test('unsafe persisted fields fail before shell execution', {skip:process.platform==='win32'}, () => {
  for(const env of [{CTYUN_AK:'forbidden'}, {CTYUN_WORKER_REGION_ID:'x\nBAD=1'}]) {
    const result=spawnSync('bash',['-eu','-c','source "$OPS/worker-deployment-profile.sh"; worker_profile_load bot-test'],{encoding:'utf8',env:{...process.env,OPS:ops,CATSCO_WORKER_DEPLOYMENT_JSON:JSON.stringify({profile:'public_ip',env})}});
    assert.notEqual(result.status,0);
  }
});

test('artifact synchronization includes both resource pools and refuses partial replacement', {skip:process.platform==='win32'}, t => {
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'worker-sync-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const bin=path.join(root,'bin');fs.mkdirSync(bin);
  for(const [tenant,uid] of [['bot-private','11'],['bot-public','12']]) {
    fs.mkdirSync(path.join(root,tenant));fs.writeFileSync(path.join(root,tenant,'inject.env'),`CATSCO_BOT_UID=${uid}\n`);
  }
  fs.writeFileSync(path.join(root,'bot-public/deployment.json'),JSON.stringify(publicProfile));
  fs.writeFileSync(path.join(bin,'ctyun-cli'),`#!/usr/bin/env node
const a=process.argv.slice(2);const val=k=>a[a.indexOf(k)+1];
const name=val('--instanceName');const pub=name==='worker-bot-public';
if(val('--regionID') !== (pub?'foshan':'nat'))process.exit(9);
if(pub&&process.env.FAIL_PUBLIC)process.exit(8);
process.stdout.write(JSON.stringify({statusCode:800,returnObj:{results:[{instanceName:name,instanceStatus:'running',fixedIPList:['10.0.0.3'],floatingIP:pub?'8.8.8.8':''}]}}));`,{mode:0o755});
  const routes=path.join(root,'routes.json');const routeScript=path.join(bin,'route');
  fs.writeFileSync(routeScript,'#!/bin/bash\ncat > "$ROUTES"\n',{mode:0o755});
  const env={...process.env,PATH:bin+':'+process.env.PATH,CTYUN_WORKER_STATE_ROOT:root,CTYUN_WORKER_REGION_ID:'nat',CTYUN_WORKER_PROJECT_ID:'default',CATSCO_ARTIFACT_GATEWAY_ENABLED:'1',CATSCO_ARTIFACT_GATEWAY_ROUTE_SCRIPT:routeScript,ROUTES:routes};
  const run=extra=>spawnSync('bash',[path.join(ops,'sync-artifact-gateway-routes.sh')],{encoding:'utf8',env:{...env,...extra}});
  const result=run({});assert.equal(result.status,0,result.stderr);
  const before=fs.readFileSync(routes,'utf8');const parsed=JSON.parse(before);
  assert.equal(parsed['11'].network_mode,'private');assert.equal(parsed['12'].network_mode,'public');assert.equal(parsed['12'].private_ip,'8.8.8.8');
  const failed=run({FAIL_PUBLIC:'1'});assert.notEqual(failed.status,0);assert.equal(fs.readFileSync(routes,'utf8'),before);
});
