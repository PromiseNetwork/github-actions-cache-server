import type { CacheMetricsTags } from '~/lib/metrics'

import { z } from 'zod'
import { listEntriesByKey, useDB } from '~/lib/db'
import { ENV } from '~/lib/env'
import { getMetrics, METRICS } from '~/lib/metrics'

const queryParamSchema = z.object({
  key: z.string().min(1),
})

export default defineEventHandler(async (event) => {
  const parsedQuery = queryParamSchema.safeParse(getQuery(event))
  if (!parsedQuery.success)
    throw createError({
      statusCode: 400,
      statusMessage: `Invalid query parameters: ${parsedQuery.error.message}`,
    })

  const { key } = parsedQuery.data

  const db = await useDB()
  const entries = await listEntriesByKey(db, key)

  // Record cache hit/miss metrics
  if (ENV.METRICS_ENABLED) {
    try {
      const metrics = getMetrics()
      const operation = entries.length > 0 ? 'hit' : 'miss'
      const tags: CacheMetricsTags = {
        operation,
        storage_driver: ENV.STORAGE_DRIVER,
      }
      metrics.increment(METRICS.CACHE.OPERATIONS_TOTAL, 1, tags)
    } catch (err) {
      // Don't fail the request if metrics fail
      console.warn('Failed to record cache metrics:', err)
    }
  }

  return {
    totalCount: entries.length,
    artifactCaches: entries.map((entry) => ({
      cacheKey: entry.key,
      cacheVersion: entry.version,
    })),
  }
})
