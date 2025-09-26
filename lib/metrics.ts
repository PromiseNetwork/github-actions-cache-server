import { StatsD } from 'hot-shots'
import { logger } from '~/lib/logger'

export interface MetricsConfig {
  enabled: boolean
  host: string
  port: number
  prefix: string
  globalTags: Record<string, string>
}

class MetricsClient {
  private client: StatsD | null = null
  private config: MetricsConfig

  constructor(config: MetricsConfig) {
    this.config = config

    if (config.enabled) {
      this.client = new StatsD({
        host: config.host,
        port: config.port,
        prefix: config.prefix,
        globalTags: config.globalTags,
        errorHandler: (error) => {
          logger.warn('StatsD error:', error)
        },
      })
      logger.info(`Metrics enabled - sending to ${config.host}:${config.port}`)
    } else {
      logger.info('Metrics disabled')
    }
  }

  increment(metric: string, value: number = 1, tags?: Record<string, string>): void {
    if (!this.client) return
    this.client.increment(metric, value, this.formatTags(tags))
  }

  histogram(metric: string, value: number, tags?: Record<string, string>): void {
    if (!this.client) return
    this.client.histogram(metric, value, this.formatTags(tags))
  }

  gauge(metric: string, value: number, tags?: Record<string, string>): void {
    if (!this.client) return
    this.client.gauge(metric, value, this.formatTags(tags))
  }

  timing(metric: string, value: number, tags?: Record<string, string>): void {
    if (!this.client) return
    this.client.timing(metric, value, this.formatTags(tags))
  }

  private formatTags(tags?: Record<string, string>): string[] | undefined {
    if (!tags) return undefined
    return Object.entries(tags).map(([key, value]) => `${key}:${value}`)
  }

  close(): void {
    if (this.client) {
      this.client.close()
    }
  }
}

let metricsInstance: MetricsClient | null = null

export function initializeMetrics(config: MetricsConfig): void {
  if (metricsInstance) {
    metricsInstance.close()
  }
  metricsInstance = new MetricsClient(config)
}

export function getMetrics(): MetricsClient {
  if (!metricsInstance) {
    throw new Error('Metrics not initialized. Call initializeMetrics() first.')
  }
  return metricsInstance
}

export interface HttpMetricsTags extends Record<string, string> {
  method: string
  endpoint: string
  status_code: string
}

export interface CacheMetricsTags extends Record<string, string> {
  operation: 'hit' | 'miss' | 'upload' | 'download'
  storage_driver: string
}

export const METRICS = {
  HTTP: {
    REQUESTS_TOTAL: 'http.requests.total',
    RESPONSE_TIME: 'http.response_time',
  },
  CACHE: {
    OPERATIONS_TOTAL: 'cache.operations.total',
    SIZE_BYTES: 'cache.size_bytes',
    UPLOAD_CHUNKS: 'cache.upload_chunks.total',
  },
} as const
