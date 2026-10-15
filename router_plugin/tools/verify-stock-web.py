#!/usr/bin/env python3
"""Real browser check: stock menu, existing login, configuration and no extra port."""
import argparse
import json
from pathlib import Path
import subprocess
from stock_web import StockWeb,settings_credentials

p=argparse.ArgumentParser()
p.add_argument('--settings',type=Path,required=True)
p.add_argument('--ssh',default='localrouter')
p.add_argument('--screenshots',type=Path,default=Path(__file__).resolve().parents[1]/'artifacts/browser')
args=p.parse_args()
web=StockWeb(lambda cmd:subprocess.check_output(['ssh',args.ssh,cmd]),settings_credentials(args.settings))
args.screenshots.mkdir(parents=True,exist_ok=True)
script=r'''
const fs=require('fs');
const input=JSON.parse(fs.readFileSync(0,'utf8'));
const assert=require('assert');
const pw=require(input.playwright);
(async()=>{
 const browser=await pw.chromium.launch({headless:true});
 try {
  const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1280,height:1000}});

  const page=await context.newPage();const errors=[];const failed=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('response',r=>{if(r.status()>=400)failed.push({url:new URL(r.url()).pathname,status:r.status()});});
  await page.goto(input.origin+'/Main_Login.asp');
  await page.locator('[name=login_username]').fill(input.username);
  await page.locator('[name=login_passwd]').fill(input.password);
  await Promise.all([page.waitForURL('**/index.asp'),page.locator('.button[onclick="login();"]').click()]);
  const menu=page.getByText('虚拟路由',{exact:true});
  try {await menu.first().waitFor({state:'visible',timeout:20000});}catch(e){await page.screenshot({path:input.screenshots+'/menu-failure.png',fullPage:true});console.error(JSON.stringify({url:page.url(),title:await page.title(),errors,failed,body:(await page.locator('body').innerText()).slice(0,1200)}));throw e;}
  await page.goto(input.origin+'/Main_Netstat_Content.asp',{referer:input.origin+'/index.asp'});
  await page.locator('#FormTitle').waitFor({state:'visible'});
  const baseline={banner:await page.locator('.banner1').boundingBox(),menu:await page.locator('#mainMenu').boundingBox(),form:await page.locator('#FormTitle').boundingBox()};
  await page.getByText('虚拟路由',{exact:true}).first().click();
  await page.waitForURL('**/user/cloudhelper-router/VirtualRouter.asp');
  await page.locator('#status').filter({hasText:'已停用'}).waitFor({timeout:15000});
  const positions=await page.evaluate(()=>{
   const nodes=[...document.querySelectorAll('#mainMenu a, #mainMenu div')];
   const a=nodes.filter(n=>n.textContent.trim()==='网络工具').pop();
   const b=nodes.filter(n=>n.textContent.trim()==='虚拟路由').pop();
   return {network:a?a.getBoundingClientRect().top:null,virtual:b?b.getBoundingClientRect().top:null};
  });
  assert(positions.network!==null && positions.virtual>positions.network,'menu placement');
  const layout={banner:await page.locator('.banner1').boundingBox(),menu:await page.locator('#mainMenu').boundingBox(),form:await page.locator('#FormTitle').boundingBox()};
  for(const part of ['banner','menu','form']){assert(Math.abs(layout[part].x-baseline[part].x)<2,part+' horizontal alignment');assert(Math.abs(layout[part].width-baseline[part].width)<2,part+' width');}
  assert.equal(await page.locator('#admin_key').count(),0);
  assert.equal(new URL(page.url()).port,new URL(input.origin).port);
  assert(await page.locator('#mainMenu').innerText());
  const originalDNS={};
  for(const [key,id]of Object.entries({doh:'dns_doh',dot:'dns_dot',dns:'dns_upstream'})){originalDNS[key]=await page.locator('#'+id).inputValue();assert(originalDNS[key],key+' loaded');}
  assert(originalDNS.doh.startsWith('https://'),'DoH HTTPS');
  const originalPort=await page.locator('#port').inputValue();
  const temporaryPort=originalPort==='12041'?'12042':'12041';
  try {
   const saved=page.waitForResponse(r=>r.url().endsWith('/api.asp') && r.request().postDataJSON().action==='configure');
   await page.locator('#port').fill(temporaryPort);await page.locator('#save').click();
   const reply=await (await saved).json();assert(reply.ok);assert.deepEqual(reply.config.dns_upstreams,originalDNS);
   await page.reload();await page.waitForFunction(p=>document.getElementById('port').value===p,temporaryPort);
  } finally {
   await page.waitForFunction(()=>!!window.csrf);
   const saved=page.waitForResponse(r=>r.url().endsWith('/api.asp') && r.request().postDataJSON().action==='configure');
   await page.locator('#port').fill(originalPort);await page.locator('#save').click();
   assert((await (await saved).json()).ok);
  }
  await page.screenshot({path:input.screenshots+'/stock-menu-desktop.png',fullPage:true});
  await page.locator('#gateway_tab').click();
  await page.locator('#gateway_panel').waitFor({state:'visible'});
  const originalGateway=await page.evaluate(async()=>{const r=await new Promise(resolve=>api({action:'status'},resolve));return r.config.gateway_proxy;});
  assert(originalGateway,'gateway configuration loaded');
  const temporaryGateway={enabled:false,proxy_cidrs:['192.168.60.0/24','10.23.0.0/16'],allowed_node_ids:['7','14']};
  try {
   await page.evaluate(g=>{document.getElementById('gateway_enabled').checked=g.enabled;document.getElementById('gateway_cidrs').value=g.proxy_cidrs.join('\n');gatewayNodes=g.allowed_node_ids;renderGatewayNodes();},temporaryGateway);
   const saved=page.waitForResponse(r=>r.url().endsWith('/api.asp')&&r.request().postDataJSON().action==='configure');
   await page.locator('#gateway_save').click();const response=await saved;
   assert.deepEqual(response.request().postDataJSON().config,{gateway_proxy:temporaryGateway});
   assert.deepEqual((await response.json()).config.gateway_proxy,temporaryGateway);
   await page.reload();await page.waitForFunction(()=>loaded);
   assert.equal(await page.locator('#gateway_cidrs').inputValue(),temporaryGateway.proxy_cidrs.join('\n'));
   await page.locator('#gateway_tab').click();
   await page.screenshot({path:input.screenshots+'/stock-gateway-desktop.png',fullPage:true});
  } finally {
   const restored=await page.evaluate(g=>new Promise(resolve=>api({action:'configure',csrf:csrf,config:{gateway_proxy:g}},resolve)),originalGateway);
   assert(restored.ok);assert.deepEqual(restored.config.gateway_proxy,originalGateway);
   await page.reload();await page.waitForFunction(()=>loaded);
  }
  await page.setViewportSize({width:1722,height:1000});
  const wideBanner=await page.locator('.banner1').boundingBox();
  const wideMenu=await page.locator('#mainMenu').boundingBox();
  assert(Math.abs((wideMenu.x-wideBanner.x)-(layout.menu.x-layout.banner.x))<2,'wide viewport alignment');
  await page.screenshot({path:input.screenshots+'/stock-menu-wide.png',fullPage:true});
  await page.setViewportSize({width:390,height:844});
  await page.screenshot({path:input.screenshots+'/stock-menu-mobile.png',fullPage:true});
  assert.equal(errors.length,0,JSON.stringify(errors));
  assert.equal(failed.length,0,JSON.stringify(failed));
  console.log(JSON.stringify({result:'PASS',menuBelowNetworkTools:true,existingAsusSession:true,separateManagementPort:false,saveAndReload:true,gatewayPartialSaveAndReload:true,configurationRestored:true,stockLayoutAligned:true,dnsUpstreamsLoadedAndPreserved:true,scriptErrors:errors,failedRequests:failed}));
 } finally {await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
'''
username,password=settings_credentials(args.settings)
data={'username':username,'password':password,'cookie':web.cookie,'origin':web.origin,'screenshots':str(args.screenshots.resolve()).replace('\\','/'),
      'playwright':str(Path.home()/'.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright')}
result=subprocess.run(['node','-e',script],input=json.dumps(data),encoding='utf-8',capture_output=True,timeout=100)
print(result.stdout,end='')
if result.returncode:
    print(result.stderr.replace(password,'[REDACTED]'),end='');raise SystemExit(result.returncode)
(args.screenshots/'verification.json').write_text(result.stdout,encoding='utf-8')
