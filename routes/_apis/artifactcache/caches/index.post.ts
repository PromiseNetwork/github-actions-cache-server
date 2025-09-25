import type { CacheMetricsTags } from '~/lib/metrics'

import { z } from 'zod'
import { ENV } from '~/lib/env'
import { getMetrics, METRICS } from '~/lib/metrics'
import { useStorageAdapter } from '~/lib/storage'

const bodySchema = z.object({
  key: z.string().min(1),
  version: z.string(),
})

export default defineEventHandler(async (event) => {
  const body = (await readBody(event)) as unknown
  const parsedBody = bodySchema.safeParse(body)
  if (!parsedBody.success)
    throw createError({
      statusCode: 400,
      statusMessage: `Invalid body: ${parsedBody.error.message}`,
    })

  const { key, version } = parsedBody.data

  const adapter = await useStorageAdapter()
  const result = await adapter.reserveCache({ key, version })

  // Record cache creation metrics
  if (ENV.METRICS_ENABLED) {
    try {
      const metrics = getMetrics()
      const tags: CacheMetricsTags = {
        operation: 'upload',
        storage_driver: ENV.STORAGE_DRIVER,
      }
      metrics.increment(METRICS.CACHE.OPERATIONS_TOTAL, 1, tags)
    } catch (err) {
      console.warn('Failed to record cache creation metrics:', err)
    }
  }

  return result
})
