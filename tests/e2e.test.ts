import crypto from 'node:crypto'
import fs from 'node:fs/promises'
import path from 'node:path'

import { restoreCache, saveCache } from '@actions/cache'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import { cacheApi } from '~/tests/utils'

const TEST_TEMP_DIR = path.join(import.meta.dirname, 'temp')
await fs.mkdir(TEST_TEMP_DIR, { recursive: true })
const testFilePath = path.join(TEST_TEMP_DIR, 'test.bin')

const MB = 1024 * 1024

const versions = ['v2', 'v1'] as const

for (const version of versions) {
  describe(`save and restore cache with @actions/cache package with api ${version}`, () => {
    beforeAll(() => {
      if (version !== 'v2') return

      process.env.ACTIONS_CACHE_SERVICE_V2 = 'true'
      process.env.ACTIONS_RUNTIME_TOKEN = 'mock-runtime-token'
    })
    afterAll(() => {
      delete process.env.ACTIONS_CACHE_SERVICE_V2
      delete process.env.ACTIONS_RUNTIME_TOKEN
    })

    for (const size of [1, 5 * MB, 50 * MB])
      test(`${size} Bytes`, { timeout: 90_000 }, async () => {
        // save
        const expectedContents = crypto.randomBytes(size)
        await fs.writeFile(testFilePath, expectedContents)
        await saveCache([testFilePath], 'cache-key')
        await fs.rm(testFilePath)

        // restore
        const cacheHitKey = await restoreCache([testFilePath], 'cache-key')
        expect(cacheHitKey).toBe('cache-key')

        // check contents
        const restoredContents = await fs.readFile(testFilePath)
        expect(restoredContents.compare(expectedContents)).toBe(0)
      })
  })
}

test(
  'pruning cache via API',
  {
    timeout: 60_000,
  },
  async () => {
    // Reserve cache via v1 API
    const reserveRes = await cacheApi
      .post('caches', {
        json: { key: 'prune-test-key', version: 'prune-test-version' },
      })
      .json<{ cacheId: number | null }>()

    expect(reserveRes.cacheId).toBeTruthy()
    const cacheId = reserveRes.cacheId!

    // Upload a chunk via v1 API
    const chunkData = new Uint8Array(crypto.randomBytes(1024))
    await cacheApi.patch(`caches/${cacheId}`, {
      body: chunkData,
      headers: {
        'content-range': `bytes 0-${chunkData.length - 1}/*`,
        'content-type': 'application/octet-stream',
      },
    })

    // Commit via v1 API
    await cacheApi.post(`caches/${cacheId}`, {
      json: { size: chunkData.length },
    })

    // Verify cache entry exists
    const getRes = await cacheApi.get('cache', {
      searchParams: { keys: 'prune-test-key', version: 'prune-test-version' },
    })

    expect(getRes.status).toBe(200)
    const entry = await getRes.json<{ archiveLocation: string; cacheKey: string }>()
    expect(entry.cacheKey).toBe('prune-test-key')
    expect(entry.archiveLocation).toContain('/download/')

    // Verify we can download it
    const downloadRes = await fetch(entry.archiveLocation)
    expect(downloadRes.status).toBe(200)
    const downloadedData = new Uint8Array(await downloadRes.arrayBuffer())
    expect(Buffer.from(downloadedData).compare(Buffer.from(chunkData))).toBe(0)
  },
)
