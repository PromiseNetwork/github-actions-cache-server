import type { HttpMetricsTags } from '~/lib/metrics'

import cluster from 'node:cluster'
import { H3Error } from 'h3'
import { useDB } from '~/lib/db'
import { ENV } from '~/lib/env'
import { logger } from '~/lib/logger'
import { getMetrics, initializeMetrics, METRICS } from '~/lib/metrics'
import { useStorageAdapter } from '~/lib/storage'

function getEndpointName(path: string): string {
  // Normalize dynamic routes for better metric grouping
  return path
    .replace(/\/_apis\/artifactcache/, '/api/cache') // Simplify API paths first
    .split('/')
    .map((segment) => {
      if (!segment) return segment
      // Numeric IDs
      if (/^\d+$/.test(segment)) return ':id'
      // UUIDs
      if (/^[a-f0-9-]{36}$/.test(segment)) return ':uuid'
      // Long hex strings (cache file names, hashes)
      if (/^[a-f0-9]{32,}$/.test(segment)) return ':hash'
      // Long alphanumeric strings (base64, cache keys, etc)
      if (segment.length > 20 && /^[\w+/=-]+$/.test(segment)) return ':param'
      return segment
    })
    .join('/')
}

export default defineNitroPlugin(async (nitro) => {
  const version = useRuntimeConfig().version
  if (cluster.isPrimary) logger.info(`🚀 Starting GitHub Actions Cache Server (${version})`)

  await useDB()
  await useStorageAdapter()

  // Initialize metrics
  initializeMetrics({
    enabled: ENV.METRICS_ENABLED,
    host: ENV.METRICS_HOST,
    port: ENV.METRICS_PORT,
    prefix: ENV.METRICS_PREFIX,
    globalTags: {
      service: 'github-actions-cache-server',
      version: version || 'unknown',
      storage_driver: ENV.STORAGE_DRIVER,
      db_driver: ENV.DB_DRIVER,
    },
  })

  // Track HTTP request metrics
  nitro.hooks.hook('request', (event) => {
    if (ENV.METRICS_ENABLED) {
      event.context._startTime = Date.now()
    }
  })

  // eslint-disable-next-line @shopify/prefer-early-return
  nitro.hooks.hook('afterResponse', (event) => {
    if (ENV.METRICS_ENABLED && event.context._startTime) {
      const duration = Date.now() - event.context._startTime
      const statusCode = getResponseStatus(event).toString()
      const endpoint = getEndpointName(event.path)

      const tags: HttpMetricsTags = {
        method: event.method || 'UNKNOWN',
        endpoint,
        status_code: statusCode,
      }

      try {
        const metrics = getMetrics()
        metrics.increment(METRICS.HTTP.REQUESTS_TOTAL, 1, tags)
        metrics.timing(METRICS.HTTP.RESPONSE_TIME, duration, tags)
      } catch (err) {
        logger.warn('Failed to record HTTP metrics:', err)
      }
    }
  })

  nitro.hooks.hook('error', (error, { event }) => {
    if (!event) {
      logger.error(error)
      return
    }

    const statusCode = error instanceof H3Error ? error.statusCode : 500

    // Record error metrics
    if (ENV.METRICS_ENABLED && event.context._startTime) {
      const duration = Date.now() - event.context._startTime
      const endpoint = getEndpointName(event.path)

      const tags: HttpMetricsTags = {
        method: event.method || 'UNKNOWN',
        endpoint,
        status_code: statusCode.toString(),
      }

      try {
        const metrics = getMetrics()
        metrics.increment(METRICS.HTTP.REQUESTS_TOTAL, 1, tags)
        metrics.timing(METRICS.HTTP.RESPONSE_TIME, duration, tags)
      } catch (err) {
        logger.warn('Failed to record error metrics:', err)
      }
    }

    logger.error(`Response: ${event.method} ${event.path} > ${statusCode}\n`, error)
  })

  if (ENV.DEBUG) {
    nitro.hooks.hook('request', (event) => {
      logger.debug(`Request: ${event.method} ${event.path}`)
    })
    nitro.hooks.hook('afterResponse', (event) => {
      logger.debug(`Response: ${event.method} ${event.path} > ${getResponseStatus(event)}`)
    })
  }

  if (!version) throw new Error('No version found in runtime config')

  if (cluster.isPrimary) {
    const db = await useDB()
    const existing = await db
      .selectFrom('meta')
      .where('key', '=', 'version')
      .select('value')
      .executeTakeFirst()

    if (!existing || existing.value !== version) {
      logger.info(
        `Version changed from ${existing?.value ?? '[no version, first install]'} to ${version}. Pruning cache...`,
      )
      const adapter = await useStorageAdapter()
      await adapter.pruneCaches()
    }

    if (existing) {
      await db.updateTable('meta').set('value', version).where('key', '=', 'version').execute()
    } else {
      await db.insertInto('meta').values({ key: 'version', value: version }).execute()
    }
  }

  if (process.send && cluster.isPrimary) process.send('nitro:ready')
})
