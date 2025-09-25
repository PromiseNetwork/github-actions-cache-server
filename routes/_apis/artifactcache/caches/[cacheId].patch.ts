import type { Buffer } from 'node:buffer'

import type { CacheMetricsTags } from '~/lib/metrics'
import { z } from 'zod'
import { ENV } from '~/lib/env'
import { logger } from '~/lib/logger'
import { getMetrics, METRICS } from '~/lib/metrics'
import { useStorageAdapter } from '~/lib/storage'

const pathParamsSchema = z.object({
  cacheId: z.coerce.number(),
})

export default defineEventHandler(async (event) => {
  const parsedPathParams = pathParamsSchema.safeParse(event.context.params)
  if (!parsedPathParams.success)
    throw createError({
      statusCode: 400,
      statusMessage: `Invalid path parameters: ${parsedPathParams.error.message}`,
    })

  const { cacheId } = parsedPathParams.data

  const stream = getRequestWebStream(event)
  if (!stream) {
    logger.debug('Upload: Request body is not a stream')
    throw createError({ statusCode: 400, statusMessage: 'Request body must be a stream' })
  }

  const contentRangeHeader = getHeader(event, 'content-range')
  if (!contentRangeHeader) {
    logger.debug("Upload: 'content-range' header not found")
    throw createError({ statusCode: 400, statusMessage: "'content-range' header is required" })
  }

  const { start, end } = parseContentRangeHeader(contentRangeHeader)
  if (Number.isNaN(start) || Number.isNaN(end)) {
    logger.debug(`Upload: Invalid 'content-range' header (${contentRangeHeader})`)
    throw createError({ statusCode: 400, statusMessage: 'Invalid content-range header' })
  }

  // TODO find a better way to calculate chunk size
  // this should be the correct chunk size except for the last chunk
  // this should handle the incorrect chunk size of the last chunk by just setting it to the limit of 10000 (for s3)
  const chunkIndex = Math.min(Math.floor(start / (end - start)), 9999)

  const adapter = await useStorageAdapter()
  await adapter.uploadChunk({
    uploadId: cacheId,
    chunkStream: stream as ReadableStream<Buffer>,
    chunkStart: start,
    chunkIndex,
  })

  // Record chunk upload metrics and cache size
  if (ENV.METRICS_ENABLED) {
    try {
      const metrics = getMetrics()
      const chunkSize = end - start + 1
      const tags: CacheMetricsTags = {
        operation: 'upload',
        storage_driver: ENV.STORAGE_DRIVER,
      }
      metrics.increment(METRICS.CACHE.UPLOAD_CHUNKS, 1, tags)
      metrics.histogram(METRICS.CACHE.SIZE_BYTES, chunkSize, tags)
    } catch (err) {
      console.warn('Failed to record upload metrics:', err)
    }
  }
})

function parseContentRangeHeader(contentRange: string) {
  const [start, end] = contentRange.replace('bytes', '').replace('/*', '').trim().split('-')
  return { start: Number.parseInt(start), end: Number.parseInt(end) }
}
