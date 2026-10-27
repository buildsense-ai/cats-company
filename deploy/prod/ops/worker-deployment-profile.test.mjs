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

// 2026-10-27 regression: the console hands out shared-cluster containers and
// dedicated instances from one fleet, so every snapshot minted by the server
// carries CATSCO_WORKER_BACKEND. The jq guard did not learn the key, so every
// lifecycle call (update/renew/reset/rollback/destroy/billing) died with
// "invalid worker deployment snapshot" for workers whose snapshot was newer
// than the release that introduced the selector.
const serverSnapshotEnv = {
  CTYUN_WORKER_REGION_ID:'200000004421',CTYUN_WORKER_PROJECT_ID:'enterprise',CTYUN_IMAGE_PROJECT_ID:'enterprise',
  CTYUN_WORKER_AZ_NAME:'cn-gd-fos7-1a-public-citycloud',CTYUN_WORKER_FLAVOR_ID:'flavor',CTYUN_WORKER_VPC_ID:'vpc',
  CTYUN_WORKER_SUBNET_ID:'subnet',CTYUN_WORKER_SECURITY_GROUP_ID:'sg',CTYUN_WORKER_EXT_IP:'0',
  CTYUN_WORKER_BILLING_MODE:'month',CTYUN_WORKER_CYCLE_COUNT:'1',
  CTYUN_JUMP_IP:'jump',CTYUN_JUMP_PORT:'22',CTYUN_JUMP_USER:'root',CTYUN_JUMP_KEY:'/var/lib/catsco-worker/jump_host_ed25519',
  CATSCO_WORKER_HTTP_BASE_URL:'https://app.catsco.cn',CATSCO_WORKER_SERVER_URL:'wss://app.catsco.cn/v0/channels',
  CATSCO_WORKER_BACKEND:'ecs',
};

test('server-shaped snapshot carrying the backend selector loads for every profile', {skip:process.platform==='win32'}, () => {
  const script = `source "$OPS/worker-deployment-profile.sh"
worker_profile_load bot-test
printf '%s|%s|' "$CATSCO_WORKER_BACKEND" "$CTYUN_WORKER_REGION_ID"`;
  for (const profile of ['private_nat','public_ip','packed_shared']) {
    const result=spawnSync('bash',['-eu','-c',script],{encoding:'utf8',env:{...process.env,OPS:ops,CATSCO_WORKER_DEPLOYMENT_JSON:JSON.stringify({profile,env:serverSnapshotEnv})}});
    assert.equal(result.status,0,`${profile}: ${result.stderr}`);
    assert.equal(result.stdout.trim(),'ecs|200000004421',profile);
  }
});

test('an unlisted key still fails the snapshot guard', {skip:process.platform==='win32'}, () => {
  const env={...serverSnapshotEnv,CTYUN_AK:'must-never-travel'};
  const result=spawnSync('bash',['-eu','-c','source "$OPS/worker-deployment-profile.sh"; worker_profile_load bot-test'],{encoding:'utf8',env:{...process.env,OPS:ops,CATSCO_WORKER_DEPLOYMENT_JSON:JSON.stringify({profile:'private_nat',env})}});
  assert.notEqual(result.status,0);
});
