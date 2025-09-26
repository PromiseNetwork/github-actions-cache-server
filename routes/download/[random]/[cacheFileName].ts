import type { CacheMetricsTags } from '~/lib/metrics'

import type { CacheFileName } from '~/lib/storage/storage-driver'
import { z } from 'zod'
import { ENV } from '~/lib/env'
import { getMetrics, METRICS } from '~/lib/metrics'
import { useStorageAdapter } from '~/lib/storage'

const pathParamsSchema = z.object({
  cacheFileName: z.string(),
})

export default defineEventHandler(async (event) => {
  const parsedPathParams = pathParamsSchema.safeParse(event.context.params)
  if (!parsedPathParams.success)
    throw createError({
      statusCode: 400,
      statusMessage: `Invalid path parameters: ${parsedPathParams.error.message}`,
    })

  const { cacheFileName } = parsedPathParams.data

  const adapter = await useStorageAdapter()
  const stream = await adapter.download(cacheFileName as CacheFileName)
  if (!stream) {
    throw createError({
      statusCode: 404,
      message: 'Cache file not found',
    })
  }

  // Record cache download metrics
  if (ENV.METRICS_ENABLED) {
    try {
      const metrics = getMetrics()
      const tags: CacheMetricsTags = {
        operation: 'download',
        storage_driver: ENV.STORAGE_DRIVER,
      }
      metrics.increment(METRICS.CACHE.OPERATIONS_TOTAL, 1, tags)
    } catch (err) {
      console.warn('Failed to record cache download metrics:', err)
    }
  }

  return sendStream(event, stream)
})
