import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SolutionLibrary from '../SolutionLibrary.vue'
import SelectionWorkspace from '../SelectionWorkspace.vue'
import { selectionChannels } from '@/api/productSelections'
import { getSolution, listSolutions, type SolutionSummary } from '@/api/solutionLibrary'

const auth = vi.hoisted(() => ({ isAdmin:true, isAuthenticated:true, user:{id:1} }))
vi.mock('@/stores/auth', () => ({ useAuthStore:() => auth }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({t:(s:string)=>s}) }))
vi.mock('@/api/solutionLibrary',async () => ({...await vi.importActual<typeof import('@/api/solutionLibrary')>('@/api/solutionLibrary'),
  listSolutions:vi.fn(),getSolution:vi.fn(),getSolutionAudit:vi.fn(),getSolutionOffers:vi.fn()}))
const row:SolutionSummary={id:'gpt',name:'GPT',purpose:'处理文件',parts:['订阅'],prerequisite:'自有账号',gap:'工时',stage:'priority',
  assembly:'先识别，再用样本验收。', choices:[{offer_id:'one',name:'扫描件 OCR',listing:'离线识别工具',role:'optional',
    solves:'扫描页不能搜索',condition:'仅在没有文本层时搭配',status:'untested'}],
  delivery:'untested',priority:1,channels:[selectionChannels[0]],types:['订阅'],base_cost:null,target:null,incomplete:false,review:false,retired:false}
beforeEach(()=>{vi.clearAllMocks();auth.isAdmin=true;vi.mocked(listSolutions).mockResolvedValue({version:'v1',solutions:[row]})})
describe('admin solution library',()=>{
  it('loads summaries first, filters locally and renders merchant text as text',async()=>{
    vi.mocked(getSolution).mockResolvedValue({availability:{},detail:{
      solution:{...row, goal_key:'goal',path_key:'path',customer:'客户',difference:'路径',observed:null,journey:{purchase:'购',activate:'开',use:'用',exception:'异',renew:'续'},
        commercial:{competition:'竞',information:'信',acquisition:'获',repeat:'复',costs:'费',aftercare:'后'},components:[{id:'p',name:'订阅',kind:'product',role:'required',status:'merchant',offers:['one'],depends_on:[],condition:'自有账号'}],problems:[],evidence:[]},
      version:'v1',analyzed_at:'',snapshot_at:'',revision:1,notes:'',conflicts:[],evidence:[],offers:[{id:'one',name:'商家',shop_id:'s',shop:'店',url:'javascript:alert(1)',type:'订阅',category:'',minimum:1,cost:null,quote_at:'',detail_at:'',detail_state:'read',original:'<img src=x onerror=bad()>',disposition:'component',reason:'来源'}]}})
    const w=mount(SolutionLibrary)
    await flushPromises()
    expect(getSolution).not.toHaveBeenCalled()
    const group=w.get('[data-choice-group]')
    expect(group.text()).toContain('扫描件 OCR')
    expect(group.text()).toContain('扫描页不能搜索')
    expect(group.text()).toContain('仅在没有文本层时搭配')
    expect(group.element.compareDocumentPosition(w.get('article').element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(w.get('article').text()).toContain(row.assembly)
    expect(w.text()).toContain('solutions.unknown')
    await w.get('input[type=search]').setValue('other');expect(w.find('article').exists()).toBe(false)
    await w.get('input[type=search]').setValue('ocr')
    await w.get('[data-choice-group] button').trigger('click');await flushPromises()
    expect(getSolution).toHaveBeenCalledOnce()
    const source=w.findAll('details').find(e=>e.element.querySelector(':scope > summary')?.textContent?.includes('solutions.sources'))!
    source.element.open=true;await source.trigger('toggle')
    expect(w.text()).toContain('<img src=x onerror=bad()>');expect(w.find('img').exists()).toBe(false)
    expect(w.find('a[href^="javascript:"]').exists()).toBe(false)
    await w.get('article > button').trigger('click');await w.get('article > button').trigger('click')
    expect(getSolution).toHaveBeenCalledOnce();w.unmount()
  })
  it('keeps the products and their services together when paging and searching',async()=>{
    const scenarios=Array.from({length:4},(_,i)=>({...row,id:'use-'+i,name:'场景 '+i}))
    vi.mocked(listSolutions).mockResolvedValue({version:'v1',solutions:scenarios})
    const w=mount(SolutionLibrary)
    await flushPromises()
    expect(w.findAll('[data-choice-group]')).toHaveLength(3)
    await w.get('nav button:last-child').trigger('click')
    expect(w.get('[data-choice-group]').attributes('data-choice-group')).toBe('use-3')
    expect(w.get('article').attributes('data-solution')).toBe('use-3')
    await w.get('input[type=search]').setValue('场景 0')
    expect(w.get('[data-choice-group]').attributes('data-choice-group')).toBe('use-0')
    expect(getSolution).not.toHaveBeenCalled()
    w.unmount()
  })
  it('mounts personal records for ordinary users without requesting admin data',async()=>{
    auth.isAdmin=false
    const w=mount(SelectionWorkspace,{global:{stubs:{ProductSelectionPanel:true}}})
    await flushPromises();expect(listSolutions).not.toHaveBeenCalled();expect(w.find('nav').exists()).toBe(false);w.unmount()
  })
})
