<template>
  <div v-if="!detailed" data-testid="video-usage" class="space-y-0.5 text-xs">
    <div class="whitespace-nowrap font-medium text-gray-900 dark:text-white">
      {{ countText }} · {{ durationText }}
    </div>
    <div class="whitespace-nowrap text-gray-500 dark:text-gray-400">
      {{ t('usage.videoResolution') }} {{ resolution }}
    </div>
  </div>
  <div v-else data-testid="video-cost-details" class="space-y-1.5">
    <div class="flex items-center justify-between gap-4">
      <span class="text-gray-400">{{ t('usage.videoCount') }}</span>
      <span class="font-medium text-white">{{ countText }}</span>
    </div>
    <div class="flex items-center justify-between gap-4">
      <span class="text-gray-400">{{ t('usage.videoDuration') }}</span>
      <span class="font-medium text-white">{{ durationText }}</span>
    </div>
    <div class="flex items-center justify-between gap-4">
      <span class="text-gray-400">{{ t('usage.videoResolution') }}</span>
      <span class="font-medium text-white">{{ resolution }}</span>
    </div>
    <template v-if="row.billing_mode === BILLING_MODE_VIDEO">
      <div class="flex items-center justify-between gap-4">
        <span class="text-gray-400">{{ t('usage.videoPricePerSecond') }}</span>
        <span class="font-medium text-sky-300">
          {{ unitPrice === null ? missing : `$${unitPrice.toFixed(8)}${t('usage.perSecond')}` }}
        </span>
      </div>
      <div v-if="unitPrice !== null" class="space-y-1 border-t border-gray-700 pt-1.5">
        <div class="text-gray-400">{{ t('usage.videoBillingFormula') }}</div>
        <div class="font-medium text-white">
          ${{ unitPrice.toFixed(8) }}{{ t('usage.perSecond') }} × {{ billedSeconds }} {{ t('usage.secondUnit') }} = ${{ row.total_cost.toFixed(8) }}
        </div>
      </div>
    </template>
    <div v-else-if="row.billing_mode === BILLING_MODE_PER_REQUEST" class="flex items-center justify-between gap-4">
      <span class="text-gray-400">{{ t('usage.unitPrice') }}</span>
      <span class="font-medium text-sky-300">${{ row.total_cost.toFixed(8) }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { BILLING_MODE_PER_REQUEST, BILLING_MODE_VIDEO } from '@/utils/billingMode'
import { positiveVideoValue, videoBilledSeconds, videoPricePerSecond, type VideoUsageRow } from '@/utils/videoUsage'

const props = defineProps<{ row: VideoUsageRow; detailed?: boolean }>()
const { t } = useI18n()
const missing = computed(() => t('usage.videoMetadataMissing'))
const countText = computed(() => {
  const count = positiveVideoValue(props.row.video_count)
  return count === null ? `${t('usage.videoCount')} ${missing.value}` : `${count} ${t('usage.videoUnit')}`
})
const durationText = computed(() => {
  const seconds = positiveVideoValue(props.row.video_duration_seconds)
  return seconds === null ? `${t('usage.videoDuration')} ${missing.value}` : `${seconds} ${t('usage.secondUnit')}`
})
const resolution = computed(() => props.row.video_resolution?.trim() || missing.value)
const billedSeconds = computed(() => videoBilledSeconds(props.row))
const unitPrice = computed(() => videoPricePerSecond(props.row))
</script>
