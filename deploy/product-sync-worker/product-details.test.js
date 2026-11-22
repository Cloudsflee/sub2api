const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const crypto = require('node:crypto')
const { ProductDetails, fetchDescription, extractDescription } = require('./product-details')

const row = key => ({ id: crypto.createHash('sha256').update('shop:' + key).digest('hex').slice(0,24), shop_id: 'shop', goods_key: key })
const now = Date.now()
const at = new Date(now).toISOString()
const control = async () => ({ control: { mode:'adaptive' }, worker: { updated_at:at,available_lane_count:6 },
  adaptive: { history:[{ at,worker_memory_ratio:.5,host_available_ratio:.4,cpu_ratio:.3,disk_free_bytes:2e9 }] } })
test('only anonymous public fields are retained, identity and access restrictions are honored', async () => {
  const p = row('a')
  const payload = { code:1,data:{ goods:{ goods_key:'a',description:'<img onerror=alert(1)>',content:'paid article' },token:'not public' } }
  assert.deepEqual(extractDescription(payload,p),{ state:'read', original:'<img onerror=alert(1)>' })
  assert.equal(extractDescription(payload,row('b')).state,'failed')
  let requests = 0
  const result = await fetchDescription(p,{ signal:new AbortController().signal, fetch: async (url,opts) => {
    requests++; assert.equal(url.pathname,'/shopApi/Shop/goodsInfo'); assert.equal(opts.credentials,'omit'); assert.equal(opts.redirect,'manual')
    return new Response('restricted',{status:403})
  } })
  assert.equal(requests,1); assert.equal(result.state,'blocked')
})
test('one idle job, per-product durability, resume and resource hold', async t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(),'solution-details-'))
  t.after(() => fs.rmSync(root,{recursive:true,force:true}))
  const manifest = {schema:1,enabled:true,id:'test',snapshot_sha256:'f'.repeat(64), products:[row('one'),row('two')]}
  fs.writeFileSync(path.join(root,'product-details.json'),JSON.stringify(manifest))
  let calls=0, fail=true
  const options={now:()=>now,lock:()=>()=>{}, save(file,value) {
    if(file.endsWith('status.json') && value.cursor===1 && fail) {fail=false;throw new Error('disk full')}
    fs.writeFileSync(file,JSON.stringify(value))
  }}
  const args={control,take:async()=>{},signal:new AbortController().signal,read:async()=>{calls++;return {state:'read',original:'商家说明'}}}
  const job=new ProductDetails(root,options)
  assert.equal(await job.run({...args,control:async()=>({})}),false);assert.equal(calls,0)
  const fresh=new ProductDetails(root,options)
  const pending=fresh.run(args)
  assert.equal(await fresh.run(args),false)
  await assert.rejects(pending,/disk full/);assert.equal(calls,1)
  const restart=new ProductDetails(root,options)
  assert.equal(await restart.run(args),true);assert.equal(calls,1)
  assert.equal(await restart.run({...args,read:async()=>({state:'blocked',reason:'HTTP 403'})}),true)
  assert.equal(await new ProductDetails(root,options).run(args),false)
  fs.writeFileSync(path.join(root,'product-details.json'),JSON.stringify({...manifest,resume:1}))
  assert.equal(await new ProductDetails(root,options).run(args),true)
  assert.equal(JSON.parse(fs.readFileSync(path.join(root,'product-details/test/status.json'))).state,'complete')
})
