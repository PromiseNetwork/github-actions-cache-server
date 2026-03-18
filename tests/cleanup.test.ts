// Rewritten from the original white-box tests that directly imported Node
// internals (useDB, findKeyMatch, findStaleKeys, pruneKeys, touchKey,
// updateOrCreateKey, useStorageAdapter). Those modules no longer exist after
// the Go rewrite, so these tests now exercise the same behaviors through the
// public HTTP API. Key differences:
//
// - "setting last accessed date" → "create and retrieve cache entry":
//   The original tested updateOrCreateKey/touchKey setting accessed_at timestamps
//   directly on the DB. Now tests the full reserve→upload→commit→lookup flow
//   through the API, verifying the entry is retrievable and downloadable.
//
// - "getting stale keys" → "list entries by key" / "duplicate reserve":
//   The original tested findStaleKeys with controlled dates. The stale-key
//   pruning logic is now tested indirectly via the prune test in e2e.test.ts.
//   These tests instead cover listing and reservation idempotency.

import crypto from 'node:crypto'

import { describe, expect, test } from 'vitest'
import { cacheApi } from '~/tests/utils'

async function createCacheEntry(key: string, version: string) {
  const reserveRes = await cacheApi
    .post('caches', { json: { key, version } })
    .json<{ cacheId: number | null }>()
  if (!reserveRes.cacheId) throw new Error(`Failed to reserve cache for ${key}`)

  const data = new Uint8Array(crypto.randomBytes(64))
  await cacheApi.patch(`caches/${reserveRes.cacheId}`, {
    body: data,
    headers: {
      'content-range': `bytes 0-${data.length - 1}/*`,
      'content-type': 'application/octet-stream',
    },
  })

  await cacheApi.post(`caches/${reserveRes.cacheId}`, {
    json: { size: data.length },
  })
}

async function lookupCache(key: string, version: string) {
  const res = await cacheApi.get('cache', {
    searchParams: { keys: key, version },
  })
  if (res.status === 204) return null
  return res.json<{ archiveLocation: string; cacheKey: string }>()
}

describe('cache entry lifecycle', () => {
  test('create and retrieve cache entry', async () => {
    const key = `lifecycle-${crypto.randomUUID()}`
    const version = crypto.randomBytes(10).toString('hex')

    await createCacheEntry(key, version)

    const entry = await lookupCache(key, version)
    expect(entry).toBeDefined()
    expect(entry!.cacheKey).toBe(key)
    expect(entry!.archiveLocation).toContain('/download/')
  })

  test('download returns correct content', async () => {
    const key = `download-verify-${crypto.randomUUID()}`
    const version = crypto.randomBytes(10).toString('hex')
    const data = new Uint8Array(crypto.randomBytes(256))

    const reserveRes = await cacheApi
      .post('caches', { json: { key, version } })
      .json<{ cacheId: number | null }>()
    expect(reserveRes.cacheId).toBeTruthy()

    await cacheApi.patch(`caches/${reserveRes.cacheId}`, {
      body: data,
      headers: {
        'content-range': `bytes 0-${data.length - 1}/*`,
        'content-type': 'application/octet-stream',
      },
    })

    await cacheApi.post(`caches/${reserveRes.cacheId}`, {
      json: { size: data.length },
    })

    const entry = await lookupCache(key, version)
    expect(entry).toBeDefined()

    const downloadRes = await fetch(entry!.archiveLocation)
    expect(downloadRes.status).toBe(200)
    const downloaded = new Uint8Array(await downloadRes.arrayBuffer())
    expect(Buffer.from(downloaded).compare(Buffer.from(data))).toBe(0)
  })

  test('list entries by key', async () => {
    const key = `list-test-${crypto.randomUUID()}`
    const version1 = crypto.randomBytes(10).toString('hex')
    const version2 = crypto.randomBytes(10).toString('hex')

    await createCacheEntry(key, version1)
    await createCacheEntry(key, version2)

    const res = await cacheApi
      .get('caches', { searchParams: { key } })
      .json<{ totalCount: number; artifactCaches: { cacheKey: string; cacheVersion: string }[] }>()

    expect(res.totalCount).toBe(2)
    expect(res.artifactCaches).toHaveLength(2)
    const keys = res.artifactCaches.map((c) => c.cacheKey)
    expect(keys).toContain(key)
  })

  test('duplicate reserve returns null cacheId', async () => {
    const key = `dup-reserve-${crypto.randomUUID()}`
    const version = crypto.randomBytes(10).toString('hex')

    const res1 = await cacheApi
      .post('caches', { json: { key, version } })
      .json<{ cacheId: number | null }>()
    expect(res1.cacheId).toBeTruthy()

    // Second reserve with same key+version should return null
    const res2 = await cacheApi
      .post('caches', { json: { key, version } })
      .json<{ cacheId: number | null }>()
    expect(res2.cacheId).toBeNull()
  })
})
