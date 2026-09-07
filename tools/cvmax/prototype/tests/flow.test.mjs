import test from 'node:test';
import assert from 'node:assert/strict';
import {initialState,reducer,restoreState} from '../src/flow.mjs';
const send=(s,type,extra={})=>reducer(s,{type,...extra});
test('缺失答案不可报告完成，完整答案经核验后才完成',()=>{
 let s=send(initialState(),'APPLY');assert.equal(s.stage,'missing');assert.ok(s.error);
 s=send(s,'DRAFT',{key:'salary',value:'30000'});s=send(s,'APPLY');assert.equal(s.stage,'missing');
 s=send(s,'DRAFT',{key:'relocation',value:'可以讨论'});s=send(s,'APPLY');assert.equal(s.stage,'verifying');
 assert.equal(send(s,'VERIFY_DONE').stage,'complete');
});
test('连接未开启时重试不会继续，开启后自动核验',()=>{
 let s=send(initialState('connection'),'CHECK_DONE');s=send(s,'CHECK_CONNECTION');assert.equal(s.stage,'service_wait');assert.ok(s.error);
 s=send(s,'ENABLE_SERVICE');assert.equal(s.stage,'connection_verify');assert.equal(send(s,'CONNECTION_OK').stage,'filling');
});
test('错误账号不写资料；登录后需先核验',()=>{
 let s=send(initialState('login'),'CHECK_LOGIN');assert.equal(s.stage,'login_wait');
 s=send(s,'DRAFT',{key:'account',value:'其他演示用户'});s=send(s,'LOGIN');assert.equal(s.loggedIn,false);assert.ok(s.error);
 s=send(s,'DRAFT',{key:'account',value:'林知远'});s=send(s,'LOGIN');assert.equal(s.stage,'login_verify');
});
test('主动暂停后，登录完成仍等待用户恢复',()=>{
 let s=send(initialState('login'),'PAUSE');s=send(s,'LOGIN');assert.equal(s.stage,'paused');assert.equal(s.loggedIn,true);
 assert.equal(send(s,'RESUME').stage,'login_verify');
});
test('停止阻断延迟回执，核对有界结束并标记未知',()=>{
 let s=initialState('stop'),old=s.epoch;s=send(s,'STOP');assert.equal(s.stage,'stopping');
 assert.equal(send(s,'UPLOAD_DONE',{epoch:old}),s);s=send(s,'STOP_CHECK_DONE');assert.equal(s.upload,'unknown');assert.equal(s.stage,'stopped');
 assert.equal(send(s,'RESUME'),s);assert.equal(send(s,'CHECK_CONNECTION'),s);
});
test('停止后的手动核验不恢复自动化；未知附件不能宣称全部完成',()=>{
 let s=send(send(initialState('stop'),'STOP'),'STOP_CHECK_DONE');s=send(s,'TAKEOVER');
 assert.equal(send(s,'VERIFY_MANUAL').stage,'manual');s=send(s,'DRAFT',{key:'school',value:'上海交通大学'});
 s=send(send(s,'VERIFY_MANUAL'),'MANUAL_DONE');assert.equal(s.stage,'partial');assert.equal(s.stopped,true);
 s=send(s,'REVEAL_UPLOAD');assert.equal(s.stage,'stopped_done');assert.equal(s.stopped,true);assert.equal(send(s,'PAUSE'),s);
});
test('刷新保持停止；执行中的旧任务恢复为暂停',()=>{
 let s=send(initialState('stop'),'STOP');let r=restoreState(JSON.stringify(s));assert.equal(r.stage,'stopped');assert.equal(r.upload,'unknown');assert.equal(r.stopped,true);
 r=restoreState(JSON.stringify(initialState('connection')));assert.equal(r.stage,'paused');
});
test('旧场景的延迟事件不能影响新场景',()=>{
 let s=initialState('connection'),old=s.epoch;s=send(s,'RESET',{scenario:'login'});
 assert.equal(send(s,'CHECK_DONE',{epoch:old}),s);assert.equal(s.stage,'login_wait');
});
test('损坏的本地记录安全回退',()=>{
 for(const raw of [null,'{}','bad',JSON.stringify({...initialState(),fields:{name:42}})])assert.equal(restoreState(raw).stage,'missing');
});
test('核验期间用户修改字段，旧核验不能将新内容报告为完成',()=>{
 let s={...initialState(),salary:'30000',relocation:'接受',stage:'verifying'};
 s=send(s,'FIELD',{key:'email',value:''});assert.equal(s.stage,'missing');assert.equal(send(s,'VERIFY_DONE'),s);assert.ok(send(s,'APPLY').error);
 s={...initialState('stop'),stage:'manual_verify',stopped:true};s=send(s,'DRAFT',{key:'school',value:'其他学校'});assert.equal(s.stage,'manual');assert.equal(send(s,'MANUAL_DONE'),s);
});
