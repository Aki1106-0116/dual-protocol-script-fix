const {chromium}=require('playwright');
const fs=require('fs');
const root=require('path').resolve(__dirname,'..');
const previewRoot=require('path').join(root,'.test-artifacts');fs.mkdirSync(previewRoot,{recursive:true});
const index=fs.readFileSync(root+'/web/index.html','utf8');
const login=fs.readFileSync(root+'/web/login.html','utf8');
const config={stack_mode:'reality_hy2',main_mode:'reality',node_name:'Private Edge',fp:'chrome',reality_address:'203.0.113.10',reality_sni:'www.example.com',reality_uuid:'123e4567-e89b-12d3-a456-426614174000',reality_public_key:'public-key',reality_short_id:'abcdef',reality_private_key_configured:true,rotate_reality_keys:false,hy2_domain:'hy.example.com',hy2_password:'test-secret',hy2_port_mode:'single',hy2_port:45678,hy2_cert_source:'acme',hy2_acme_email:'test@example.com',hy2_obfs_type:'none',hy2_gecko_min_packet_size:512,hy2_gecko_max_packet_size:1200};
const status={router:{stack:'reality_hy2',routes:[{protocol:'vless',label:'VLESS · REALITY · Vision',slot:0,mode:'direct',ready:true},{protocol:'hy2',label:'Hysteria2',slot:1,mode:'vpngate',ready:true,country:'JP',exit_ip:'203.0.113.20'}]},exits:[{slot:1,port:31234,node:{hostname:'vpn-demo',country:'Japan',country_code:'JP'},status:'up',exit_ip:'203.0.113.20'}],storage_issues:[],max_exits:12};
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.DPS_TEST_CHROME?{executablePath:process.env.DPS_TEST_CHROME}:{})});
 const page=await browser.newPage({viewport:{width:1440,height:1000},deviceScaleFactor:1});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>d.accept());
 let loginMode=false;const calls=[];
 await page.route('https://panel.test/**',async route=>{
  const req=route.request(),url=new URL(req.url());calls.push({path:url.pathname,body:req.postData()});
  if(url.pathname.includes('/assets/')){const file=url.pathname.split('/').pop();return route.fulfill({contentType:file.endsWith('.css')?'text/css':'application/javascript',body:fs.readFileSync(root+'/web/'+file,'utf8')})}
  if(!url.pathname.includes('/api/'))return route.fulfill({contentType:'text/html',body:loginMode?login:index});
  let data={ok:true};if(url.pathname.endsWith('/status'))data=status;else if(url.pathname.endsWith('/node-config')&&req.method()==='GET')data=config;else if(url.pathname.endsWith('/jobs'))data=[];else if(url.pathname.endsWith('/regions'))data=[{code:'JP',name:'Japan',available:12,best_ping:20,best_speed_mbps:200}];else if(url.pathname.endsWith('/node-links'))data={main_url:'vless://example',hy2_url:'hysteria2://example',info:'Sample nodes'};else if(url.pathname.endsWith('/panel-config')&&req.method()==='GET')data={domain:'hy.example.com',port:31234,path:'abcdefghi',max_exits:12,password_configured:true};else if(url.pathname.endsWith('/services/restart'))data={ok:true,results:{xray:'active','hysteria-server':'active'}};
  await route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
 });
 await page.goto('https://panel.test/abcdefghi/');await page.locator('.route-card').first().waitFor();
 await page.screenshot({path:previewRoot+'/panel-desktop.png',fullPage:true});
 await page.emulateMedia({colorScheme:'dark'});await page.screenshot({path:previewRoot+'/panel-dark.png',fullPage:true});await page.emulateMedia({colorScheme:'light'});
 await page.locator('#configure').click();await page.locator('#cfgNodeName').fill('Updated node');await page.locator('#saveConfig').click();await page.waitForTimeout(50);
 const save=calls.find(c=>c.path.endsWith('/node-config')&&c.body);if(!save||JSON.parse(save.body).node_name!=='Updated node')throw Error('node save did not submit');
 await page.locator('#reinstall').click();await page.locator('#cfgTarget').selectOption('xhttp');await page.locator('#cfgXHTTPDomain').fill('x.example.com');await page.locator('#saveConfig').click();await page.waitForTimeout(50);
 const reinstall=calls.find(c=>c.path.endsWith('/node-reinstall'));if(!reinstall||JSON.parse(reinstall.body).stack_mode!=='xhttp_hy2')throw Error('reinstall target broken');
 await page.locator('#panelSettings').click();await page.locator('#panelPath').fill('new-path-123');await page.locator('#savePanel').click();await page.waitForTimeout(50);if(!calls.find(c=>c.path.endsWith('/panel-config')&&c.body))throw Error('panel save missing');
 await page.locator('#serviceControl').click();await page.locator('[data-restart-service="all"]').click();await page.locator('[data-close="serviceModal"]').first().click();
 await page.locator('#share').click();await page.locator('[data-close="shareModal"]').first().click();
 await page.locator('[data-view="exitsView"]').click();await page.screenshot({path:previewRoot+'/panel-exits.png',fullPage:true});
 await page.locator('#add').click();await page.locator('#region').selectOption('JP');await page.locator('#create').click();await page.waitForTimeout(50);if(!calls.find(c=>c.path.endsWith('/provision')))throw Error('provision missing');
 await page.setViewportSize({width:390,height:844});await page.locator('[data-view="overview"]').click();await page.screenshot({path:previewRoot+'/panel-mobile.png',fullPage:true});
 if(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth))throw Error('mobile horizontal overflow');
 await page.locator('#configure').click();await page.screenshot({path:previewRoot+'/panel-config-mobile.png',fullPage:true});await page.keyboard.press('Escape');
 loginMode=true;await page.goto('https://panel.test/abcdefghi/');await page.locator('#pw').fill('test');await page.locator('#submit').click();await page.waitForTimeout(50);if(!calls.find(c=>c.path.endsWith('/login')))throw Error('login missing');
 if(errors.length)throw Error(errors.join('\n'));console.log('UI tested: login, save node, switch stack, panel settings, restart, sharing, exits, desktop/mobile layout.');
 await browser.close();
})().catch(e=>{console.error(e);process.exit(1)});
