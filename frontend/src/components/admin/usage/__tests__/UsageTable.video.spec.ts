import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import enDashboard from '@/i18n/locales/en/dashboard'
import zhDashboard from '@/i18n/locales/zh/dashboard'
import enResources from '@/i18n/locales/en/admin/resources'
import zhResources from '@/i18n/locales/zh/admin/resources'
import UsageTable from '../UsageTable.vue'

let currentLocale: 'zh' | 'en' = 'zh'
const messages = {
  zh: { ...zhDashboard, admin: zhResources },
  en: { ...enDashboard, admin: enResources },
}
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string) => key.split('.').reduce<unknown>((value, part) =>
      value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined,
    messages[currentLocale]) ?? key,
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))
vi.mock('@/utils/ipGeoLookup', () => ({
  getEntry: vi.fn(() => ({ status: 'idle' })),
  fetchOne: vi.fn(),
  fetchBatch: vi.fn(),
}))

const videoRow = {
  request_id: 'video-seconds',
  model: 'YUXI-H3-PRO',
  billing_mode: 'video',
  video_count: 1,
  video_duration_seconds: 10,
  video_resolution: '720p',
  image_count: 0,
  input_tokens: 0,
  output_tokens: 0,
  input_cost: 0,
  output_cost: 0,
  total_cost: 2.2,
  actual_cost: 2.2,
  rate_multiplier: 1,
  duration_ms: 519000,
}

const DataTableStub = {
  props: ['data'],
  template: `<div><div v-for="row in data" :key="row.request_id">
    <div data-testid="mode-cell"><slot name="cell-billing_mode" :row="row" /></div>
    <div data-testid="usage-cell"><slot name="cell-tokens" :row="row" /></div>
    <div data-testid="cost-cell"><slot name="cell-cost" :row="row" /></div>
  </div></div>`,
}

function renderVideo(options: {
  locale?: 'zh' | 'en'
  showAccountBilling?: boolean
  row?: Record<string, unknown>
} = {}) {
  currentLocale = options.locale ?? 'zh'
  return mount(UsageTable, {
    props: {
      data: [{ ...videoRow, ...options.row }],
      loading: false,
      columns: [],
      showAccountBilling: options.showAccountBilling ?? true,
    },
    global: {
      stubs: { DataTable: DataTableStub, Icon: true, Teleport: true },
    },
  })
}

describe('video usage in the shared admin/user table', () => {
  it.each([
    ['zh', true, '按秒（视频）', '10 秒', '1 条'],
    ['zh', false, '按秒（视频）', '10 秒', '1 条'],
    ['en', true, 'Per second (video)', '10 s', '1 video'],
    ['en', false, 'Per second (video)', '10 s', '1 video'],
  ] as const)('shows recorded video units in %s with admin costs %s', (locale, showAccountBilling, label, duration, count) => {
    const wrapper = renderVideo({ locale, showAccountBilling })
    expect(wrapper.get('[data-testid="mode-cell"]').text()).toBe(label)
    const usage = wrapper.get('[data-testid="usage-cell"]')
    expect(usage.text()).toContain(count)
    expect(usage.text()).toContain(duration)
    expect(usage.text()).toContain('720p')
    expect(usage.text()).not.toContain('519')
    expect(usage.find('.cursor-help').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the recorded per-second price and calculation in the cost tooltip', async () => {
    const wrapper = renderVideo()
    await wrapper.get('[data-testid="cost-cell"] .group').trigger('mouseenter')
    const tooltip = wrapper.get('.fixed')
    expect(tooltip.text()).toContain('标准每秒单价')
    expect(tooltip.text()).toContain('$0.22000000')
    expect(tooltip.text()).toContain('10 秒')
    expect(tooltip.text()).toContain('$2.20000000')
    expect(tooltip.text()).toContain('计费分辨率')
    wrapper.unmount()
  })

  it('keeps fixed-price videos per request even with the legacy image counter', async () => {
    const wrapper = renderVideo({
      row: { model: 'MX-SD-2.5-B', billing_mode: 'per_request', image_count: 1, total_cost: 8.5, actual_cost: 8.5 },
    })
    expect(wrapper.get('[data-testid="mode-cell"]').text()).toBe('按次')
    expect(wrapper.get('[data-testid="usage-cell"]').text()).toContain('10 秒')
    expect(wrapper.get('[data-testid="usage-cell"]').text()).not.toContain('张')
    await wrapper.get('[data-testid="cost-cell"] .group').trigger('mouseenter')
    expect(wrapper.get('.fixed').text()).not.toContain('每秒单价')
    expect(wrapper.get('.fixed').text()).toContain('$8.50000000')
    wrapper.unmount()
  })

  it('retains token usage and token prices for token-billed videos', async () => {
    const wrapper = renderVideo({
      row: { billing_mode: 'token', input_tokens: 100, output_tokens: 500, input_cost: 0.1, output_cost: 0.5 },
    })
    expect(wrapper.get('[data-testid="mode-cell"]').text()).toBe('按量')
    const usage = wrapper.get('[data-testid="usage-cell"]')
    expect(usage.text()).toContain('10 秒')
    expect(usage.text()).toContain('100')
    expect(usage.text()).toContain('500')
    await wrapper.get('[data-testid="cost-cell"] .group').trigger('mouseenter')
    expect(wrapper.get('.fixed').text()).toContain('/ 1M Token')
    expect(wrapper.get('.fixed').text()).not.toContain('每秒单价')
    wrapper.unmount()
  })

  it('does not invent seconds or a unit price when historical metadata is absent', async () => {
    const wrapper = renderVideo({
      row: { video_count: 0, video_duration_seconds: null, video_resolution: null },
    })
    expect(wrapper.get('[data-testid="usage-cell"]').text()).toContain('未记录')
    expect(wrapper.get('[data-testid="usage-cell"]').text()).not.toContain('519')
    await wrapper.get('[data-testid="cost-cell"] .group').trigger('mouseenter')
    const tooltip = wrapper.get('.fixed').text()
    expect(tooltip).not.toContain('/秒 ×')
    expect(tooltip).not.toMatch(/NaN|Infinity/)
    wrapper.unmount()
  })

  it('uses billed seconds across all outputs and preserves the separate multiplier', async () => {
    const wrapper = renderVideo({
      row: { video_count: 2, total_cost: 4.4, actual_cost: 2.2, rate_multiplier: 0.5 },
    })
    await wrapper.get('[data-testid="cost-cell"] .group').trigger('mouseenter')
    const tooltip = wrapper.get('.fixed').text()
    expect(tooltip).toContain('$0.22000000')
    expect(tooltip).toContain('20 秒')
    expect(tooltip).toContain('$4.40000000')
    expect(tooltip).toContain('$2.20000000')
    expect(tooltip).toContain('0.50x')
    wrapper.unmount()
  })

  it('displays a zero per-second rate for free video output', async () => {
    const wrapper = renderVideo({ row: { total_cost: 0, actual_cost: 0 } })
    await wrapper.get('[data-testid="cost-cell"] .group').trigger('mouseenter')
    const tooltip = wrapper.get('.fixed').text()
    expect(tooltip).toContain('标准每秒单价')
    expect(tooltip).toContain('$0.00000000')
    wrapper.unmount()
  })
})
