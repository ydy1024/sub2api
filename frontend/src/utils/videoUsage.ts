import type { UsageLog } from '@/types'
import { BILLING_MODE_VIDEO } from './billingMode'

export type VideoUsageRow = Pick<UsageLog,
  'billing_mode' | 'video_count' | 'video_duration_seconds' | 'video_resolution' | 'total_cost'
>

export function positiveVideoValue(value: number | null | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null
}

export function videoBilledSeconds(row: VideoUsageRow): number | null {
  const count = positiveVideoValue(row.video_count)
  const duration = positiveVideoValue(row.video_duration_seconds)
  return count !== null && duration !== null ? positiveVideoValue(count * duration) : null
}

export function videoPricePerSecond(row: VideoUsageRow): number | null {
  const seconds = videoBilledSeconds(row)
  if (row.billing_mode !== BILLING_MODE_VIDEO || seconds === null) return null
  if (!Number.isFinite(row.total_cost) || row.total_cost < 0) return null
  // 用结算时保存的标准费用反推单价，避免当前配置或用户倍率改变历史明细。
  const price = row.total_cost / seconds
  return Number.isFinite(price) ? price : null
}
