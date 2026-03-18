// Rewritten from the original white-box tests that directly called
// updateOrCreateKey() and findKeyMatch() on the Node DB layer. Those modules
// no longer exist after the Go rewrite, so these tests now exercise key
// matching through the public HTTP API (GET /cache?keys=...&version=...).
//
// The test cases are 1:1 with the originals:
//   - exact primary match
//   - exact restore key match (via comma-separated keys param)
//   - prefixed restore key match
//   - multiple restore keys returns first match
//   - prefixed match returns newest key
//   - exact match preferred over prefix match
//
// Each test uses randomized keys (crypto.randomUUID) to avoid collisions,
// replacing the original beforeEach(pruneKeys) cleanup pattern.

import crypto from 'node:crypto'

import { describe, expect, test } from 'vitest'
import { cacheApi, sleep } from '~/tests/utils'

// Reserve → upload → commit a cache entry through the API
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

// Helper to look up a cache entry via the API
async function lookupCache(keys: string, version: string) {
  const res = await cacheApi.get('cache', {
    searchParams: { keys, version },
  })
  if (res.status === 204) return null
  return res.json<{ archiveLocation: string; cacheKey: string }>()
}

describe('key matching via API', () => {
  const version = crypto.randomBytes(10).toString('hex')

  test('exact primary match', async () => {
    const key = `exact-primary-${crypto.randomUUID()}`
    await createCacheEntry(key, version)

    const match = await lookupCache(key, version)
    expect(match).toBeDefined()
    expect(match!.cacheKey).toBe(key)
  })

  test('exact restore key match', async () => {
    const keyA = `restore-exact-a-${crypto.randomUUID()}`
    const keyB = `restore-exact-b-${crypto.randomUUID()}`
    await createCacheEntry(keyA, version)

    // Look up keyB with keyA as restore key
    const match = await lookupCache(`${keyB},${keyA}`, version)
    expect(match).toBeDefined()
    expect(match!.cacheKey).toBe(keyA)
  })

  test('prefixed restore key match', async () => {
    const prefix = `pfx-restore-${crypto.randomUUID()}`
    await createCacheEntry(`${prefix}-a`, version)

    // Look up with prefix as restore key
    const match = await lookupCache(`${prefix}-b,${prefix}`, version)
    expect(match).toBeDefined()
    expect(match!.cacheKey).toBe(`${prefix}-a`)
  })

  test('restore key match with multiple keys returns first match', async () => {
    const keyA = `multi-restore-a-${crypto.randomUUID()}`
    const keyB = `multi-restore-b-${crypto.randomUUID()}`
    await createCacheEntry(keyA, version)
    await createCacheEntry(keyB, version)

    const match = await lookupCache(`no-match-${crypto.randomUUID()},${keyA},${keyB}`, version)
    expect(match).toBeDefined()
    expect(match!.cacheKey).toBe(keyA)
  })

  test('prefixed restore key match returns newest key', async () => {
    const prefix = `pfx-newest-${crypto.randomUUID()}`
    await createCacheEntry(`${prefix}-a`, version)
    await sleep(50)
    await createCacheEntry(`${prefix}-b`, version)

    const match = await lookupCache(`${prefix}-c,${prefix}`, version)
    expect(match).toBeDefined()
    expect(match!.cacheKey).toBe(`${prefix}-b`)
  })

  test('restore key prefers exact match over prefixed match', async () => {
    const prefix = `pfx-exact-pref-${crypto.randomUUID()}`
    await createCacheEntry(prefix, version)
    await sleep(50)
    await createCacheEntry(`${prefix}-a`, version)

    const match = await lookupCache(`${prefix}-b,${prefix}`, version)
    expect(match).toBeDefined()
    expect(match!.cacheKey).toBe(prefix)
  })
})
